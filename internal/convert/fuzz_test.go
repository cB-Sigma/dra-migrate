package convert

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/cb-Sigma/dra-migrate/internal/lint"
	"github.com/cb-Sigma/dra-migrate/internal/manifest"
)

// FuzzConvert feeds arbitrary input through lint and convert. Whatever the
// input, the tool must not panic, a converted file must still parse, and
// converting it again must change nothing.
func FuzzConvert(f *testing.F) {
	inputs, _ := filepath.Glob("../../testdata/golden/*/input.yaml")
	for _, p := range inputs {
		if data, err := os.ReadFile(p); err == nil {
			f.Add(data)
		}
	}
	for _, doc := range layouts {
		f.Add([]byte(doc))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		file := manifest.Parse("fuzz.yaml", "fuzz.yaml", data)
		lint.Run([]*manifest.File{file}, lint.Options{})
		for _, seg := range file.Segments {
			if seg.Err != nil {
				// Broken documents are passed through untouched, so the
				// output cannot be expected to parse either.
				return
			}
		}
		for _, opts := range []Options{{}, {ShareAcrossContainers: true, MapNodeSelectors: true, ClaimMode: ModeClaim, APIVersion: APIv1beta1}} {
			res, err := Convert([]*manifest.File{manifest.Parse("fuzz.yaml", "fuzz.yaml", data)}, opts)
			if err != nil {
				return
			}
			fr := res.Files[0]
			if !fr.Changed {
				continue
			}
			out := manifest.Parse("fuzz.yaml", "fuzz.yaml", fr.Output)
			for _, seg := range out.Segments {
				if seg.Err != nil {
					t.Fatalf("converted output does not parse: %v\n--- input ---\n%s\n--- output ---\n%s", seg.Err, data, fr.Output)
				}
			}
			again, err := Convert([]*manifest.File{out}, opts)
			if err != nil {
				t.Fatalf("second conversion failed: %v", err)
			}
			if again.Files[0].Changed {
				t.Fatalf("conversion is not idempotent\n--- input ---\n%s\n--- first ---\n%s\n--- second ---\n%s", data, fr.Output, again.Files[0].Output)
			}
		}
	})
}
