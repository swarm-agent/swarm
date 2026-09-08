package tool

import (
 "context"
 "encoding/json"
 "errors"
 "reflect"
 "testing"
 "time"

 pebblestore "swarm/packages/swarmd/internal/store/pebble"
)

// Requirement: ReconcileParts is a whole-project-only constrained edit shared
// by primary and managed dispatch. Prevent policy override, malformed locators,
// and publication using a stale gate. This service test observes complete bytes
// and gate postconditions; browser selector resolution is a separate runtime gate.
func TestArtifactV3RemixReconcileParts(t *testing.T) {
 ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
 defer cancel()
 manifest := pebblestore.ArtifactV3Manifest{SchemaVersion: pebblestore.ArtifactV3ManifestVersion, Entrypoint: "pages/main.html", Parts: []pebblestore.ArtifactV3Part{{ID:"old", Label:"Old", Locator:pebblestore.ArtifactV3Locator{Kind:"file", Path:"pages/main.html"}}}}
 body, _ := json.Marshal(manifest)
 repo := &artifactV3AuthorRepoFake{base:map[string][]byte{pebblestore.ArtifactV3ManifestFilename:body,"pages/main.html":[]byte("<main>Output</main>"),"assets/brand.bin":{0,1,255}}}
 service := NewArtifactV3AuthorService(t.TempDir(),repo,&artifactV3BuilderFake{},&artifactV3PreviewerFake{})
 grant := fullArtifactV3Grant()
 principal := artifactV3Principal()
 replacement := []pebblestore.ArtifactV3Part{{ID:"new",Label:"New",Locator:pebblestore.ArtifactV3Locator{Kind:"file",Path:"pages/main.html"}}}
 grant.RevisionIntent = pebblestore.ArtifactV3RevisionFocusedParts
 if err := service.ReconcileParts(ctx,principal,grant,replacement); !errors.Is(err,ErrArtifactV3AuthorLocked) { t.Fatalf("focused reconcile: %v",err) }
 grant.RevisionIntent = pebblestore.ArtifactV3RevisionWholeProject
 grant.TargetPartIDs = nil
 if err := service.Edit(ctx,principal,grant,pebblestore.ArtifactV3ManifestFilename,body,[]byte("{}"),false); !errors.Is(err,ErrArtifactV3AuthorLocked) { t.Fatalf("raw manifest edit: %v",err) }
 gate, err := service.BuildPreview(ctx,principal,grant)
 if err != nil || !gate.Ready { t.Fatalf("gate: %+v %v",gate,err) }
 for _, bad := range [][]pebblestore.ArtifactV3Part{nil, {replacement[0],replacement[0]}, {{ID:"escape",Label:"Escape",Locator:pebblestore.ArtifactV3Locator{Kind:"file",Path:"../outside"}}}} {
  if err := service.ReconcileParts(ctx,principal,grant,bad); err == nil { t.Fatal("invalid Parts accepted") }
  read, err := service.Read(ctx,principal,grant,pebblestore.ArtifactV3ManifestFilename,0,0)
  if err != nil || read.Content != string(body) { t.Fatalf("rejection mutated manifest: %v",err) }
 }
 if _, err := parseArtifactV3NativeParts([]any{map[string]any{"id":"x","label":"X","locator":map[string]any{"kind":"file","path":"pages/main.html"},"policy":"override"}}); err == nil { t.Fatal("unknown policy field accepted") }
 if err := service.ReconcileParts(ctx,principal,grant,replacement); err != nil { t.Fatal(err) }
 if _,err := service.Finish(ctx,principal,grant); !errors.Is(err,ErrArtifactV3AuthorNotReady) { t.Fatalf("stale gate: %v",err) }
 if _,err := service.BuildPreview(ctx,principal,grant); err != nil { t.Fatal(err) }
 if _,err := service.Finish(ctx,principal,grant); err != nil { t.Fatal(err) }
 var got pebblestore.ArtifactV3Manifest
 if err := json.Unmarshal(repo.submits[0].Project[pebblestore.ArtifactV3ManifestFilename],&got); err != nil { t.Fatal(err) }
 if !reflect.DeepEqual(got.Parts,replacement) || got.Entrypoint != manifest.Entrypoint || got.SchemaVersion != manifest.SchemaVersion { t.Fatalf("manifest: %+v",got) }
 if string(repo.base[pebblestore.ArtifactV3ManifestFilename]) != string(body) || !reflect.DeepEqual(repo.submits[0].Project["assets/brand.bin"],repo.base["assets/brand.bin"]) { t.Fatal("base or unrelated asset changed") }
}
