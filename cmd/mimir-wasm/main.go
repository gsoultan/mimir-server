//go:build js && wasm

// Command mimir-wasm answers with Mimir models in a browser. The web app
// runs it in a worker; it sets globalThis.mimir to load, predict and
// unload, each returning a JSON string, an object with "error" on failure.
package main

import (
	"context"
	"encoding/json"
	"syscall/js"

	"github.com/gsoultan/mimir-server/internal/device"
)

func main() {
	r := device.New()
	js.Global().Set("mimir", js.ValueOf(map[string]any{
		// load(model JSON, weights Uint8Array)
		"load": js.FuncOf(func(_ js.Value, args []js.Value) any {
			m, err := device.ParseModel(args[0].String())
			if err != nil {
				return fail(err)
			}
			if err := r.Load(m, bytes(args[1])); err != nil {
				return fail(err)
			}
			return reply(map[string]bool{"ok": true})
		}),
		// predict(id, question JSON, photo Uint8Array or null, sound
		// Uint8Array or null)
		"predict": js.FuncOf(func(_ js.Value, args []js.Value) any {
			var q device.Question
			if err := json.Unmarshal([]byte(args[1].String()), &q); err != nil {
				return fail(err)
			}
			if len(args) > 2 && args[2].Truthy() {
				q.Photo = bytes(args[2])
			}
			if len(args) > 3 && args[3].Truthy() {
				q.Sound = bytes(args[3])
			}
			a, err := r.Predict(context.Background(), args[0].String(), q)
			if err != nil {
				return fail(err)
			}
			return reply(a)
		}),
		"unload": js.FuncOf(func(_ js.Value, args []js.Value) any {
			r.Unload(args[0].String())
			return reply(map[string]bool{"ok": true})
		}),
	}))
	select {}
}

func bytes(v js.Value) []byte {
	b := make([]byte, v.Get("length").Int())
	js.CopyBytesToGo(b, v)
	return b
}

func reply(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func fail(err error) string { return reply(map[string]string{"error": err.Error()}) }
