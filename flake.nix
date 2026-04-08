{
  description = "AXIS — local-first, reservation-aware cluster substrate";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils, ... }:
    flake-utils.lib.eachDefaultSystem (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
        versionFile = builtins.readFile ./internal/buildinfo/version.go;
        versionMatch = builtins.match ''(.|\n)*Version = "([^"]+)"(.|\n)*'' versionFile;
        version =
          if versionMatch == null
          then throw "Could not extract AXIS version from internal/buildinfo/version.go"
          else builtins.elemAt versionMatch 1;

        # You may need to update this hash when dependencies change.
        # To get the new hash, set vendorHash = pkgs.lib.fakeHash; run `nix build`,
        # and copy the "got:" hash from the error message.
        vendorHash = "sha256-I4uaSAr+G8RJhgjipfb/3yO2wtQmwat6fJXXGcd57ps=";

        axis = pkgs.buildGoModule {
          pname = "axis";
          inherit version;

          src = ./.;

          inherit vendorHash;

          ldflags = [
            "-s"
            "-w"
            "-X github.com/toasterbook88/axis/internal/buildinfo.UpdateManagedBy=nix"
          ];

          doCheck = false; # tests might require network or SSH access

          meta = with pkgs.lib; {
            description = "AXIS cluster substrate";
            homepage = "https://github.com/toasterbook88/axis";
            license = licenses.mit;
            mainProgram = "axis";
          };
        };
      in {
        packages = {
          default = axis;
          axis = axis;
        };

        apps = {
          default = flake-utils.lib.mkApp { drv = axis; };
        };

        devShells.default = pkgs.mkShell {
          buildInputs = with pkgs; [
            go
            gnumake
            golangci-lint
          ];
        };
      }
    );
}
