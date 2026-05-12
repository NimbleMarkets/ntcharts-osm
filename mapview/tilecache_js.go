//go:build js

package mapview

import sm "github.com/flopp/go-staticmaps"

// configureTileCache disables go-staticmaps' on-disk tile cache under
// GOOS=js, where the WASM runtime has no filesystem and every cache
// write logs "not implemented on js".
func configureTileCache(ctx *sm.Context) { ctx.SetCache(nil) }
