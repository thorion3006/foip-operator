{
  description = "Development environment for the Netcup failover IP operator";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
  };

  outputs = { self, nixpkgs }:
    let
      systems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = nixpkgs.lib.genAttrs systems;
    in {
      devShells = forAllSystems (system:
        let
          pkgs = import nixpkgs {
            inherit system;
          };
          goPackage = pkgs.go_1_26.overrideAttrs (_: {
            version = "1.26.5";
            src = pkgs.fetchurl {
              url = "https://go.dev/dl/go1.26.5.linux-${if pkgs.stdenv.hostPlatform.system == "aarch64-linux" then "arm64" else "amd64"}.tar.gz";
              sha256 = if pkgs.stdenv.hostPlatform.system == "aarch64-linux" then "sha256-/keJ6SsfMzWGgIZLvocEKJ57tfwgfYBiPDCJNb1pbUk=" else "sha256-XCw7FsrvodloqUwdrKBKfKMBpJbZsIbhetd7uBOT8FM=";
            };
          });
        in {
          default = pkgs.mkShell {
            packages = with pkgs; [
              goPackage
              gopls
              gotools
              golangci-lint

              kubectl
              kubernetes-helm
              kind
              kustomize

              ko
              skopeo

              direnv
              nix-direnv

              git
              gnumake
              jq
              yq-go
              bashInteractive
            ];

            env = {
              CGO_ENABLED = "0";
              GOFLAGS = "-mod=readonly";
              GOTOOLCHAIN = "local";
            };

            shellHook = ''
              export PATH="$PWD/bin:$PATH"

              echo "foip-operator development shell"
              echo "Go: $(go version)"
              echo "Common commands:"
              echo "  make generate manifests fmt vet"
              echo "  make test"
              echo "  make lint"
              echo "  make helm-lint"
            '';
          };
        });

      formatter = forAllSystems (system:
        let
          pkgs = import nixpkgs { inherit system; };
        in pkgs.nixpkgs-fmt);
    };
}
