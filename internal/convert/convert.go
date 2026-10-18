// Package convert turns ONNX and GGUF model files into Mimir Model (.mim)
// files, so the server only ever stores and runs its own format.
//
// An ONNX model of the convolutional subset mim's importer knows (Conv,
// BatchNormalization, Gemm, MatMul, pooling, the common activations and
// shape operators, in float32, opsets 7 to 22) keeps its graph and runs as
// it is. A GGUF language model of the llama or qwen2 architecture (Llama
// 1 to 3, TinyLlama, Mistral, SmolLM, Qwen 2 and 2.5) gets the forward
// pass mim builds for it, with its weights kept byte for byte in their
// quantization. Anything else fails with an error that says why.
package convert

import (
	"bytes"
	"errors"
	"fmt"
	"strings"

	"github.com/gsoultan/mim"
	"github.com/gsoultan/mim/importer"
)

// Format is the kind of file an upload is.
type Format string

const (
	Mim         Format = "mim"
	ONNX        Format = "onnx"
	GGUF        Format = "gguf"
	Safetensors Format = "safetensors"
)

// Context is the longest conversation, in tokens, a converted language
// model holds. A model trained for less keeps its own length; one trained
// for more is cut to this, so its key-value cache stays a reasonable size
// (about 100 MB for a 1B model).
const Context = 4096

// ErrUnsupported reports a file that is not a model this package converts.
// Its message says why, in words fit to show the person who uploaded it.
var ErrUnsupported = errors.New("convert: unsupported model")

func unsupported(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupported, fmt.Sprintf(format, args...))
}

// Detect returns the format of a file from its leading bytes, or "" when
// it is none of them. ONNX has no magic number: a file is taken for one
// when it starts the way a serialized ModelProto does, with its
// ir_version field.
func Detect(b []byte) Format {
	switch {
	case len(b) >= 4 && (string(b[:4]) == mim.Magic || string(b[:4]) == mim.LegacyMagic):
		return Mim
	case importer.Detect(b) == "gguf":
		return GGUF
	case importer.Detect(b) == "safetensors":
		return Safetensors
	case len(b) > 1 && b[0] == 0x08: // field 1 (ir_version), varint
		return ONNX
	}
	return ""
}

// ToMim returns b as a Mimir Model file: as it is when it already is one,
// converted when it is an ONNX or GGUF model. It also returns the format b
// was in. It fails with ErrUnsupported for any other file, and for a model
// whose operators, architecture or weights can't be carried over
// faithfully.
func ToMim(b []byte) ([]byte, Format, error) {
	f := Detect(b)
	var src *importer.Source
	var err error
	switch f {
	case Mim:
		return b, f, nil
	case ONNX:
		if src, err = importer.ParseONNX(b); err != nil {
			return nil, f, unsupported("%s", trim(err))
		}
	case GGUF:
		if src, err = gguf(b); err != nil {
			return nil, f, err
		}
	case Safetensors:
		return nil, f, unsupported("a safetensors file holds weights but not the model that uses them; convert it with mim-import, or export the model to ONNX")
	default:
		return nil, f, unsupported("this isn't a .mim, ONNX or GGUF model file")
	}
	w := mim.NewWriter()
	if err := src.AddTo(w); err != nil {
		return nil, f, unsupported("%s", trim(err))
	}
	var out bytes.Buffer
	if _, err := w.WriteTo(&out); err != nil {
		return nil, f, fmt.Errorf("convert: write %s as .mim: %w", f, err)
	}
	return out.Bytes(), f, nil
}

// gguf parses a GGUF file and builds the forward pass of its language
// model, which GGUF itself doesn't carry.
func gguf(b []byte) (*importer.Source, error) {
	src, err := importer.ParseGGUF(b)
	if err != nil {
		return nil, unsupported("%s", trim(err))
	}
	arch, _ := src.Metadata["general.architecture"].(string)
	if arch != "llama" && arch != "qwen2" {
		if arch == "" {
			arch = "unnamed"
		}
		return nil, unsupported("GGUF models of the %q architecture can't be converted yet; llama (Llama, TinyLlama, Mistral, SmolLM) and qwen2 (Qwen 2 and 2.5) can", arch)
	}
	c, err := importer.LlamaConfigOf(src, 0)
	if err != nil {
		return nil, unsupported("it doesn't describe a whole %s model: %s", arch, trim(err))
	}
	opts := importer.LlamaOptions{Context: min(c.MaxContext, Context), A8: true, Cache: mim.F16}
	if _, err := importer.BuildLlama(src, opts); err != nil {
		return nil, unsupported("%s", trim(err))
	}
	return src, nil
}

// trim drops the package prefixes the importer puts on its messages.
func trim(err error) string {
	s := err.Error()
	for _, p := range []string{"importer: ", "onnx: ", "gguf: "} {
		s = strings.TrimPrefix(s, p)
	}
	return s
}
