{
  description = "wanbond — resilient WAN-bonding tunnel with adaptive FEC";

  inputs = {
    nixpkgs.url = "flake:nixpkgs";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "wanbond";
          version = "0.0.0";
          src = ./.;
          # Updated whenever go.mod dependencies change; see `nix build` error output.
          vendorHash = "sha256-uEv4hsdu8mTaqvKARC9NIBU0nXoSZjdApX/fN5pEop4=";
          subPackages = [ "cmd/wanbond" ];
          env.CGO_ENABLED = 0;
          ldflags = [ "-s" "-w" ];
          # Unit tests run via CI/Justfile; the e2e suite needs root and is never
          # part of the sandboxed package build.
          doCheck = false;
        };

        devShells.default = pkgs.mkShell {
          packages = with pkgs; [
            go
            gopls
            golangci-lint
            gnumake
            just
            # privileged e2e harness tooling
            iproute2
            util-linux # unshare / nsenter for the netns fixture
            iputils # ping
            iperf3
            tcpdump
            # P5 DPI-classification checks
            ndpi
            suricata
          ];
        };
      });
}
