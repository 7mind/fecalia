package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"
)

func compactRecoveryDoc(value string) string {
	value = strings.ReplaceAll(value, "×", "*")
	value = strings.ReplaceAll(value, "`", "")
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return unicode.ToLower(r)
	}, value)
}

func TestRecoveryDocumentationFormulaAndDecisionMapping(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	for _, name := range []string{
		"README.md",
		"docs/design.md",
		"docs/install.md",
		"docs/manual-checklist.md",
		"wanbond.example.toml",
	} {
		doc := compactRecoveryDoc(read(name))
		for _, keyword := range []string{
			"d=250ms",
			"g=10ms",
			"f=1200ms",
			"b/c/p/fgroup/lio/mtotal",
			"r/rp/i",
			"sdevice",
			"h=",
			"w=",
			"sessionid",
			"contractid",
			"outerseq",
			"ecompletion",
			"ack",
			"fallback",
			"nozero-parityinference",
		} {
			if !strings.Contains(doc, keyword) {
				t.Errorf("%s omitted canonical recovery keyword %q", name, keyword)
			}
		}
	}

	design := compactRecoveryDoc(read("docs/design.md"))
	for _, formula := range []string{
		"sdevice=a=max_path(ceil((b+c+p+(kdata+mmax+1)*lmax)/(r-rp))+i)",
		"ecompletion=max_path(ceil((p+mmax*lmax+lio)/(r-rp))+i)",
		"h=clamp(4*max(srtt",
		"w=min(d,a+h)",
	} {
		if !strings.Contains(design, formula) {
			t.Errorf("docs/design.md omitted canonical formula %q", formula)
		}
	}

	mapping := strings.ToLower(read("docs/design.md"))
	for _, decision := range []string{
		"raw sessionid/contractid",
		"not exported",
		"float64 precision",
		"identity disclosure",
		"data/control cumulative split",
		"queue_data",
		"queue_control",
		"single reconciliation authority",
	} {
		if !strings.Contains(mapping, decision) {
			t.Errorf("docs/design.md omitted request-to-source decision mapping %q", decision)
		}
	}
}
