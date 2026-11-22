{
  description = "taiga-backend: Go web API with PostgreSQL/SQLite, JWT auth, and FIT file processing";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixpkgs-unstable";
    treefmt-nix = {
      url = "github:numtide/treefmt-nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    gomod2nix = {
      url = "github:nix-community/gomod2nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    hanko = {
      url = "github:dmallubhotla/hanko";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      gomod2nix,
      hanko,
      ...
    }@inputs:
    let
      supportedSystems = [
        "x86_64-linux"
        "aarch64-linux"
        "aarch64-darwin"
        "x86_64-darwin"
      ];

      pkgsFor =
        system:
        let
          pkgs = import nixpkgs {
            inherit system;
          };
        in
        pkgs.extend (
          nixpkgs.lib.composeManyExtensions [
            gomod2nix.overlays.default
            taigaOverlay
          ]
        );

      eachSystem = f: nixpkgs.lib.genAttrs supportedSystems (system: f (pkgsFor system));

      treefmtEval = eachSystem (pkgs: inputs.treefmt-nix.lib.evalModule pkgs ./treefmt.nix);

      # Stamped by hanko; do not hand-edit (use `just release`).
      version = "0.3.0";
      commonLdflags = hanko.lib.mkGoLdflags { inherit self version; };

      taigaOverlay = final: _prev: {
        # The dependencies precompiled into a Go build cache (gomod2nix.toml's cachePackages).
        # Built without the ldflags, which stamp the commit, so it only changes with the dependencies and CI can keep it between runs; linking flags don't affect compiled packages.
        taiga-go-cache =
          (final.buildGoApplication {
            pname = "taiga";
            inherit version;
            src = ./.;
            modules = ./gomod2nix.toml;
          }).goCacheEnv;
        taiga =
          (final.buildGoApplication {
            pname = "taiga";
            inherit version;
            # Unpacks to /build/source, where the cache was built; cgo packages (go-sqlite3) miss the cache from any other directory.
            src = builtins.path {
              path = ./.;
              name = "source";
            };
            modules = ./gomod2nix.toml;
            ldflags = commonLdflags;
          }).overrideAttrs
            {
              goCacheDir = final.taiga-go-cache;
              # gomod2nix's own check drops -trimpath, so the tests would recompile every dependency, and tests one package at a time; this keeps the cache and runs them in parallel.
              checkPhase = ''
                runHook preCheck
                go test ./...
                runHook postCheck
              '';
            };
      };

      dockerImageFor =
        pkgs:
        let
          app = pkgs.taiga;
          migrations-postgres = pkgs.lib.fileset.toSource {
            root = ./.;
            fileset = ./migrations;
          };
          migrations-sqlite = pkgs.lib.fileset.toSource {
            root = ./.;
            fileset = ./migrations-sqlite;
          };
        in
        pkgs.dockerTools.buildLayeredImage {
          name = "taiga";
          # Stamped from the flake's `version` (hanko-managed). The push tags
          # in CI (`:latest`, `:master`, `:sha-…`) are layered on top by skopeo;
          # this is the immutable on-image identity.
          tag = version;
          contents = [
            pkgs.dockerTools.usrBinEnv
            # /etc/ssl/certs, so outbound TLS (OIDC discovery, S3) can verify servers.
            pkgs.dockerTools.caCertificates

            migrations-postgres
            migrations-sqlite

            pkgs.bash
            pkgs.coreutils

            app
          ];
          config = {
            # The server; `just exec-docker` overrides this with a shell.
            Cmd = [ "/bin/server" ];
            Env = [
              "PATH=/bin"
            ];
            WorkingDir = "/workspace";
          };
        };

    in
    {

      overlays.default = nixpkgs.lib.composeManyExtensions [
        gomod2nix.overlays.default
        taigaOverlay
      ];

      checks = eachSystem (pkgs: {
        formatting = treefmtEval.${pkgs.stdenv.hostPlatform.system}.config.build.check self;
        # Lint as a check: override the taiga derivation to swap go test for
        # golangci-lint in checkPhase. Reuses the vendored module setup from
        # goConfigHook so no network is needed inside the sandbox.
        golangci-lint = pkgs.taiga.overrideAttrs (old: {
          pname = "taiga-golangci-lint";
          nativeCheckInputs = (old.nativeCheckInputs or [ ]) ++ [ pkgs.golangci-lint ];
          doCheck = true;
          checkPhase = ''
            runHook preCheck
            export GOLANGCI_LINT_CACHE=$TMPDIR/golangci-lint-cache
            golangci-lint run --timeout 5m ./...
            runHook postCheck
          '';
        });
        # The package itself, whose checkPhase runs `go test`, so CI's tests and binary are one build.
        go-test = pkgs.taiga;
        # Built here so CI's image step finds it done.
        docker-image = dockerImageFor pkgs;
      });

      # nix fmt formatter
      formatter = eachSystem (pkgs: treefmtEval.${pkgs.stdenv.hostPlatform.system}.config.build.wrapper);

      packages = eachSystem (pkgs: {
        default = pkgs.taiga;
        taiga = pkgs.taiga;
        docker-image = dockerImageFor pkgs;
        # CI restores and saves this between runs (ci.yml).
        go-cache = pkgs.taiga-go-cache;
      });

      # default devshell
      devShells = eachSystem (pkgs: {
        default = pkgs.mkShell {
          packages = [
            pkgs.just
            pkgs.gomod2nix
            pkgs.go
            pkgs.gopls
            pkgs.gotools
            pkgs.golangci-lint
            pkgs.go-tools
            pkgs.sqlc
            pkgs.sqlite
            pkgs.postgresql
            pkgs.openssl

            pkgs.awscli2

            # Release tooling — `just release` invokes `hanko seal`, whose pre-commit hook runs git-cliff.
            hanko.packages.${pkgs.stdenv.hostPlatform.system}.default
            pkgs.git-cliff
          ];
        };
      });
    };
}
