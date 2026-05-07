# Usage:
# - Build the package: nix build .#yggd
# - Run the daemon from the flake: nix run github:asciimoth/ygg#yggd
# - Run helper tools: nix run github:asciimoth/ygg#yggctl -- -endpoint tcp://localhost:9001 getSelf
#   or nix run github:asciimoth/ygg#genkeys
# - Add to tmp shell: nix shell github:asciimoth/ygg#yggd
# - Add to profile: nix profile add github:asciimoth/ygg#yggd
# - Install the package in a NixOS config:
#     environment.systemPackages = [ inputs.ygg.packages.${pkgs.system}.yggd ];
# - Enable the NixOS service from this flake:
#     imports = [ inputs.ygg.nixosModules.yggd ];
#     services.yggd.enable = true;
#   The service runs yggd as root and lets the daemon read or auto-generate
#   its platform-default config file.
{
  description = "Yggdrasil Go library and daemon";
  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-unstable";
    flake-utils = {
      url = "github:numtide/flake-utils";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    pre-commit-hooks = {
      url = "github:cachix/pre-commit-hooks.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };
  outputs = {
    self,
    nixpkgs,
    flake-utils,
    pre-commit-hooks,
    ...
  }:
    flake-utils.lib.eachDefaultSystem (system: let
      pkgs = import nixpkgs {
        inherit system;
      };

      version = self.shortRev or self.dirtyShortRev or "dev";

      yggd = pkgs.buildGoModule {
        pname = "yggd";
        inherit version;
        src = ./.;
        modRoot = "yggd";
        env.GOWORK = "off";
        vendorHash = "sha256-88K/toL5kW8rMrag4UhwynutcfYhakjNEw8TtcS+s2Q=";
        subPackages = [
          "yggd"
          "yggctl"
          "genkeys"
        ];
        ldflags = [
          "-s"
          "-w"
          "-X github.com/asciimoth/ygg/ygglib/version.buildName=yggd"
          "-X github.com/asciimoth/ygg/ygglib/version.buildVersion=${version}"
        ];
        postInstall = ''
          install -Dm644 /dev/stdin "$out/lib/systemd/system/yggd.service" <<EOF
          [Unit]
          Description=Yggdrasil network daemon
          Documentation=https://yggdrasil-network.github.io/
          Wants=network-online.target
          After=network-online.target

          [Service]
          Type=simple
          ExecStart=$out/bin/yggd -logto stdout
          Restart=on-failure
          RestartSec=5s

          [Install]
          WantedBy=multi-user.target
          EOF
        '';
        meta = with pkgs.lib; {
          description = "Yggdrasil network daemon and command line tools";
          homepage = "https://github.com/asciimoth/ygg";
          license = licenses.lgpl3Only;
          mainProgram = "yggd";
          platforms = platforms.linux ++ platforms.windows;
        };
      };

      checks = {
        pre-commit-check = pre-commit-hooks.lib.${system}.run {
          src = ./.;
          hooks = {
            # gotest.enable = true;
            commitizen.enable = true;
            typos.enable = true;
            typos-commit = {
              enable = true;
              description = "Find typos in commit message";
              entry = let script = pkgs.writeShellScript "typos-commit" ''
                typos "$1"
              ''; in builtins.toString script;
              stages = [ "commit-msg" ];
            };
            govet.enable = true;
            gofmt.enable = true;
            # golangci-lint.enable = true;
            gotidy = {
              enable = true;
              description = "Makes sure go.mod matches the source code";
              entry = let script = pkgs.writeShellScript "gotidyhook" ''
                go -C ygglib mod tidy -v
                go -C yggd mod tidy -v
                go -C examples mod tidy -v
                go work sync
              ''; in builtins.toString script;
              stages = [ "pre-commit" ];
            };
            golangtest = {
              enable = true;
              description = "go test ./ygglib/... ./yggd/... ./examples/... --race";
              entry = let script = pkgs.writeShellScript "gotidyhook" ''
                go test ./ygglib/... ./yggd/... ./examples/... --race
              ''; in builtins.toString script;
              stages = [ "pre-commit" ];
            };
          };
        };
      };
    in {
      packages = {
        inherit yggd;
        default = yggd;
      };

      apps = {
        yggd = flake-utils.lib.mkApp {
          drv = yggd;
          exePath = "/bin/yggd";
        };
        yggctl = flake-utils.lib.mkApp {
          drv = yggd;
          exePath = "/bin/yggctl";
        };
        genkeys = flake-utils.lib.mkApp {
          drv = yggd;
          exePath = "/bin/genkeys";
        };
        default = flake-utils.lib.mkApp {
          drv = yggd;
          exePath = "/bin/yggd";
        };
      };

      inherit checks;

      devShells.default = pkgs.mkShell {
        inherit (checks.pre-commit-check) shellHook;
        buildInputs = with pkgs; [
          go
          golangci-lint
          gopls

          typos
          commitizen

          just

          coverage-reporter

          goreleaser
        ];
      };
    })
    // {
      nixosModules.yggd = {
        config,
        lib,
        pkgs,
        ...
      }: let
        cfg = config.services.yggd;
      in {
        options.services.yggd = {
          enable = lib.mkEnableOption "Yggdrasil network daemon";

          package = lib.mkOption {
            type = lib.types.package;
            default = self.packages.${pkgs.stdenv.hostPlatform.system}.yggd;
            defaultText = lib.literalExpression "inputs.ygg.packages.\${pkgs.stdenv.hostPlatform.system}.yggd";
            description = "Package providing the yggd daemon.";
          };

          extraArgs = lib.mkOption {
            type = lib.types.listOf lib.types.str;
            default = [];
            example = [ "-loglevel" "debug" ];
            description = "Extra command line arguments passed to yggd.";
          };
        };

        config = lib.mkIf cfg.enable {
          environment.systemPackages = [ cfg.package ];

          systemd.services.yggd = {
            description = "Yggdrasil network daemon";
            documentation = [ "https://yggdrasil-network.github.io/" ];
            wantedBy = [ "multi-user.target" ];
            wants = [ "network-online.target" ];
            after = [ "network-online.target" ];
            serviceConfig = {
              Type = "simple";
              ExecStart = "${lib.getExe cfg.package} -logto stdout ${lib.escapeShellArgs cfg.extraArgs}";
              Restart = "on-failure";
              RestartSec = "5s";
            };
          };
        };
      };

      nixosModules.default = self.nixosModules.yggd;
    };
}
