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
        in {
          default = pkgs.mkShell {
            packages = with pkgs; [
              go_1_25
              gopls
              gotools
              golangci-lint

              kubectl
              kubernetes-helm
              kind
              kustomize

              ko
              skopeo

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
