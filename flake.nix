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
#     services.yggd.settings = {
#       PrivateKeyPath = "/var/lib/yggd/private.pem";
#       Peers = [ "tls://example.net:12345" ];
#       TunType = "native";
#     };
#   The service runs yggd as root. Without configFile or settings, the daemon
#   reads or auto-generates its platform-default config file.
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
          openssh
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
        jsonFormat = pkgs.formats.json {};
        generatedConfigFile = jsonFormat.generate "yggd.conf" cfg.settings;
        effectiveConfigFile =
          if cfg.configFile != null
          then cfg.configFile
          else if cfg.settings != null
          then generatedConfigFile
          else null;
      in {
        options.services.yggd = {
          enable = lib.mkEnableOption "Yggdrasil network daemon";

          package = lib.mkOption {
            type = lib.types.package;
            default = self.packages.${pkgs.stdenv.hostPlatform.system}.yggd;
            defaultText = lib.literalExpression "inputs.ygg.packages.\${pkgs.stdenv.hostPlatform.system}.yggd";
            description = "Package providing the yggd daemon.";
          };

          configFile = lib.mkOption {
            type = lib.types.nullOr lib.types.path;
            default = null;
            example = "/etc/yggd/yggd.conf";
            description = ''
              Path to an existing HJSON or JSON daemon config file. This file is
              passed to yggd with -useconffile and is useful when the config
              contains secrets that should not be stored in the Nix store.
            '';
          };

          settings = lib.mkOption {
            type = lib.types.nullOr jsonFormat.type;
            default = null;
            example = lib.literalExpression ''
              {
                PrivateKeyPath = "/var/lib/yggd/private.pem";
                Peers = [ "tls://example.net:12345" ];
                InterfacePeers = {};
                Listen = [ "tls://0.0.0.0:0" "quic://[::]:0" ];
                AdminListen = "unix:///var/run/yggdrasil.sock";
                AdminWebListen = "127.0.0.1:9002";
                AdminWebStaticDir = "";
                LocalDNSListen = "127.0.0.1:5353";
                MulticastInterfaces = [
                  {
                    Regex = ".*";
                    Beacon = true;
                    Listen = true;
                    Port = 0;
                    Priority = 0;
                    Password = "";
                  }
                ];
                AllowedPublicKeys = [];
                Transport = {
                  DefaultNetwork = "native";
                  NetworkMappings = {
                    "*.onion" = null;
                    "*.i2p" = null;
                    "*.loki" = null;
                  };
                };
                AutoPeer = {
                  Enabled = false;
                  Sources = [ "BUILTIN" ];
                  FetchInterval = "1h";
                  CheckInterval = "1m";
                  MinimumConnected = 0;
                  MinimumConnectedFromFetch = 0;
                  Countries = [];
                  TransportSchemes = [];
                };
                Jumper = {
                  Enabled = false;
                  Addresses = [];
                  CheckInterval = "10s";
                  LinkTimeout = "30s";
                };
                TunType = "native";
                IfName = "auto";
                IfMTU = 65535;
                TunSocksListen = "127.0.0.1:1080";
                TunSocksProxies = [
                  {
                    Filter = "*.onion,*.i2p";
                    proxy_url = "socks5://[200::1]:9050";
                  }
                ];
                TunSocksDefaultProxy = "";
                TunSocksDNSFallback = "";
                TunSocksNoResolve = [];
                TunSocksTLSMITM = {
                  ca_file = "/etc/yggd/sockstun-mitm-ca.crt";
                  key_file = "/etc/yggd/sockstun-mitm-ca.key";
                  hostnames = [ "*.ygg" "*.meshname" "*.meship" "*.onion" "*.i2p" ];
                };
                TunMWO = 0;
                TunMRO = 0;
                LogLookups = false;
                NodeInfoPrivacy = false;
                NodeInfo = {};
              }
            '';
            description = ''
              Daemon configuration rendered as JSON and passed to yggd with
              -useconffile. Keys map directly to example.conf, for example
              Peers, Listen, AdminListen, AutoPeer, Jumper, Transport and TUN
              options.

              Prefer PrivateKeyPath over PrivateKey so private key material is
              not written to the Nix store. Set configFile instead when the
              whole configuration should live outside the store.
            '';
          };

          extraArgs = lib.mkOption {
            type = lib.types.listOf lib.types.str;
            default = [];
            example = [ "-loglevel" "debug" ];
            description = "Extra command line arguments passed to yggd.";
          };
        };

        config = lib.mkIf cfg.enable {
          assertions = [
            {
              assertion = !(cfg.configFile != null && cfg.settings != null);
              message = "services.yggd.configFile and services.yggd.settings are mutually exclusive.";
            }
          ];

          warnings =
            lib.optional
            (
              cfg.settings != null
              && !(builtins.hasAttr "PrivateKey" cfg.settings)
              && !(builtins.hasAttr "PrivateKeyPath" cfg.settings)
            )
            ''
              services.yggd.settings does not set PrivateKey or PrivateKeyPath.
              yggd will generate a new private key at each service start when
              reading this generated config file.
            '';

          environment.systemPackages = [ cfg.package ];

          systemd.services.yggd = {
            description = "Yggdrasil network daemon";
            documentation = [ "https://yggdrasil-network.github.io/" ];
            wantedBy = [ "multi-user.target" ];
            wants = [ "network-online.target" ];
            after = [ "network-online.target" ];
            serviceConfig = {
              Type = "simple";
              ExecStart =
                "${lib.getExe cfg.package} -logto stdout"
                + lib.optionalString (effectiveConfigFile != null) " -useconffile ${lib.escapeShellArg (toString effectiveConfigFile)}"
                + lib.optionalString (cfg.extraArgs != []) " ${lib.escapeShellArgs cfg.extraArgs}";
              Restart = "on-failure";
              RestartSec = "5s";
            };
          };
        };
      };

      nixosModules.default = self.nixosModules.yggd;
    };
}
