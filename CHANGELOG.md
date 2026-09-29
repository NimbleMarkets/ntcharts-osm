# `ntcharts-osm` CHANGELOG

## Unreleased

 * deps: upgrade ntcharts to v2.4.0 and go-booba to v0.7.0, which include Kitty shared-memory support.
 * feat(mapview): expose `Config.KittyMedium` and enable shared-memory transport for Kitty rendering in the WASM browser demo. Native demos and the default config retain direct transmission.
 * deps: raise the minimum Go version to 1.26.8 and update the Bubble Tea WASM fork to `1b36865b418a`, as required by go-booba v0.7.0.
 * deps: update Bubble Tea to v2.0.10, Bubbles to v2.2.1, Lip Gloss to v2.0.6, and the remaining application and tool dependencies to their latest compatible versions; tidy module checksums.

## v0.1.2 (2026-05-19)

 * feat(mapview): add `mapview.Model.Image()` to access the composited map `Image`.

## v0.1.1 (2026-05-14)

 * feat(mapview): add `IsMapOwnMsg` function for message routing

## v0.1.0 (2026-05-13)

 * Initial release
