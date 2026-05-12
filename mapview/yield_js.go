//go:build js

package mapview

import "time"

// yieldToJS gives the JavaScript event loop a slice so pending fetch
// promise resolutions and DOM events can drain between CPU-heavy render
// steps. On Go WASM (GOOS=js), time.Sleep with a positive duration
// schedules a setTimeout that returns control to JS until the timer
// fires; 1ms is the minimum reliably-effective resolution (sub-ms
// durations may round down to 0 and short-circuit without yielding).
//
// Called at boundaries inside the render goroutine where Go has just
// held the WASM thread for tens-to-hundreds of ms (tile composite,
// letterbox copy). Without these the browser sees a single long Go
// run and HTTP fetch resolutions / key events queue up behind it.
func yieldToJS() { time.Sleep(time.Millisecond) }
