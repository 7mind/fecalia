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
        monitorUI = pkgs.buildNpmPackage {
          pname = "wanbond-monitor-ui";
          version = "0.0.0";
          src = ./web;
          npmDepsHash = "sha256-2j+OKF3MkKtVwCsnNgmEK3b/y90V5t9gmvrmIRJXAYg=";
          installPhase = ''
            runHook preInstall
            mkdir -p "$out"
            cp -r ../internal/monitor/dist/. "$out/"
            runHook postInstall
          '';
        };
      in
      {
        packages.default = pkgs.buildGoModule {
          pname = "wanbond";
          version = "0.0.0";
          src = ./.;
          # Updated whenever go.mod dependencies or the local replaced module change;
          # see `nix build` error output.
          vendorHash = "sha256-jySdNjmizD5EG+5MsPZOPe99D8/vaarRKnmZPCOnnMY=";
          subPackages = [ "cmd/wanbond" ];
          preBuild = ''cp -r ${monitorUI}/. internal/monitor/dist/'';
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
            nodejs_24
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
