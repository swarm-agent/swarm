package artifact

import (
	"context"
	"strings"
	"testing"
)

func TestReadPackageBytesManifestAndEntry(t *testing.T) {
	body, err := canonicalPackageEntries(context.Background(), Limits{}, []PackageEntry{{Name:"index.html", Data:[]byte("<main>ready</main>")}, {Name:"assets/site.css", Data:[]byte("body{}")}})
	if err != nil { t.Fatal(err) }
	manifest, selected, err := readPackageBytes(Limits{}, body, "", 64)
	if err != nil || selected != nil || len(manifest)!=2 || manifest[0].Name!="assets/site.css" { t.Fatalf("manifest=%+v body=%q err=%v", manifest, selected, err) }
	manifest, selected, err = readPackageBytes(Limits{}, body, "index.html", 64)
	if err != nil || manifest != nil || string(selected)!="<main>ready</main>" { t.Fatalf("manifest=%+v body=%q err=%v", manifest, selected, err) }
	for _, name := range []string{"../index.html", "/index.html", `assets\site.css`, " index.html"} {
		if _, _, err := readPackageBytes(Limits{}, body, name, 64); err == nil || !strings.Contains(err.Error(), "unsafe") { t.Fatalf("unsafe %q err=%v", name, err) }
	}
}
