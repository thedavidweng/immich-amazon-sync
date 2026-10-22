package filter_test

import (
	"testing"

	"github.com/thedavidweng/immich-amazon-sync/internal/bridge"
	"github.com/thedavidweng/immich-amazon-sync/internal/filter"
)

const tagID = "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"

func image(name string) bridge.Asset {
	return bridge.Asset{ID: "one", Name: name, MIME: "image/jpeg", Kind: "IMAGE", Size: 10}
}

func video(name string) bridge.Asset {
	return bridge.Asset{ID: "two", Name: name, MIME: "video/mp4", Kind: "VIDEO", Size: 10}
}

func TestDefaultPolicyPreservesCurrentEligibility(t *testing.T) {
	p := filter.Policy{}.Normalize()
	if reason := p.Exclude(image("one.jpg")); reason != "" {
		t.Fatalf("default excluded an image: %q", reason)
	}
	if reason := p.Exclude(video("one.mp4")); reason != filter.MediaType {
		t.Fatalf("default video reason = %q", reason)
	}
	archived := image("archive.jpg")
	archived.Archived = true
	if reason := p.Exclude(archived); reason != "" {
		t.Fatalf("default excluded an archived image: %q", reason)
	}
	unknown := image("unknown.jpg")
	unknown.Size = -1
	if reason := p.Exclude(unknown); reason != "" {
		t.Fatalf("default excluded an unknown-size image: %q", reason)
	}
}

func TestMediaTypesAreInclusive(t *testing.T) {
	p := filter.Policy{MediaTypes: []string{"video"}}.Normalize()
	if reason := p.Exclude(image("one.jpg")); reason != filter.MediaType {
		t.Fatalf("image-only policy accepted an image: %q", reason)
	}
	if reason := p.Exclude(video("one.mp4")); reason != "" {
		t.Fatalf("video-only policy rejected a video: %q", reason)
	}
	both := filter.Policy{MediaTypes: []string{"image", "video"}}.Normalize()
	if both.Exclude(image("one.jpg")) != "" || both.Exclude(video("one.mp4")) != "" {
		t.Fatal("inclusive media types rejected eligible originals")
	}
	// Media type falls back to MIME when the source leaves Kind empty.
	inferred := image("")
	inferred.Kind, inferred.MIME = "", "video/mp4"
	if reason := (filter.Policy{}).Normalize().Exclude(inferred); reason != filter.MediaType {
		t.Fatalf("video MIME was treated as eligible: %q", reason)
	}
}

func TestSkipRAWUsesTheDocumentedExtensionList(t *testing.T) {
	p := filter.Policy{SkipRAW: true}.Normalize()
	for _, name := range []string{"shot.CR2", "shot.cr3", "shot.NEF", "shot.arw", "shot.dng", "shot.raf", "shot.rw2"} {
		if reason := p.Exclude(image(name)); reason != filter.RAW {
			t.Errorf("%s reason = %q, want %q", name, reason, filter.RAW)
		}
	}
	for _, name := range []string{"shot.jpg", "shot.heic", "shot.tiff", "raw"} {
		if reason := p.Exclude(image(name)); reason != "" {
			t.Errorf("%s was treated as RAW: %q", name, reason)
		}
	}
	if reason := (filter.Policy{}).Normalize().Exclude(image("shot.cr2")); reason != "" {
		t.Fatalf("skip_raw disabled but RAW excluded: %q", reason)
	}
}

func TestArchivedRuleIsExplicitAndDefaultsToIncluded(t *testing.T) {
	p := filter.Policy{IncludeArchived: new(false)}.Normalize()
	a := image("archived.jpg")
	a.Archived = true
	if reason := p.Exclude(a); reason != filter.Archived {
		t.Fatalf("archived reason = %q", reason)
	}
	if reason := p.Exclude(image("live.jpg")); reason != "" {
		t.Fatalf("live image excluded by archive rule: %q", reason)
	}
	if !(filter.Policy{}).Normalize().ArchivedIncluded() {
		t.Fatal("archive eligibility is not preserved by default")
	}
}

func TestMaximumSizeFailsClosedOnUnknownSize(t *testing.T) {
	p := filter.Policy{MaxOriginalBytes: 10}.Normalize()
	exact := image("exact.jpg")
	if reason := p.Exclude(exact); reason != "" {
		t.Fatalf("exact maximum excluded: %q", reason)
	}
	large := image("large.jpg")
	large.Size = 11
	if reason := p.Exclude(large); reason != filter.TooLarge {
		t.Fatalf("oversize reason = %q", reason)
	}
	unknown := image("unknown.jpg")
	unknown.Size = -1
	if reason := p.Exclude(unknown); reason != filter.SizeUnknown {
		t.Fatalf("unknown size was treated as zero: %q", reason)
	}
	noLimit := filter.Policy{}.Normalize()
	if reason := noLimit.Exclude(unknown); reason != "" {
		t.Fatalf("no size limit rejected unknown size: %q", reason)
	}
}

func TestExcludedTagsRequireProvenance(t *testing.T) {
	p := filter.Policy{ExcludedTags: []string{tagID}}.Normalize()
	unknown := image("unknown-tags.jpg")
	if reason := p.Exclude(unknown); reason != filter.TagUnavailable {
		t.Fatalf("missing tag provenance was treated as no tags: %q", reason)
	}
	noTags := image("no-tags.jpg")
	noTags.Tags = []bridge.Tag{}
	if reason := p.Exclude(noTags); reason != "" {
		t.Fatalf("known empty tags excluded: %q", reason)
	}
	tagged := image("tagged.jpg")
	tagged.Tags = []bridge.Tag{{ID: tagID, Name: "Private"}}
	if reason := p.Exclude(tagged); reason != filter.Tag {
		t.Fatalf("excluded tag reason = %q", reason)
	}
	other := image("other.jpg")
	other.Tags = []bridge.Tag{{ID: "cccccccc-cccc-4ccc-8ccc-cccccccccccc"}}
	if reason := p.Exclude(other); reason != "" {
		t.Fatalf("unrelated tag excluded: %q", reason)
	}
	if reason := (filter.Policy{}).Normalize().Exclude(unknown); reason != "" {
		t.Fatalf("no tag exclusion configured but unknown tags failed: %q", reason)
	}
}

func TestPolicyValidationRejectsInvalidRules(t *testing.T) {
	for name, p := range map[string]filter.Policy{
		"unknown media type": {MediaTypes: []string{"audio"}},
		"empty media type":   {MediaTypes: []string{""}},
		"duplicate media":    {MediaTypes: []string{"image", "image"}},
		"negative limit":     {MaxOriginalBytes: -1},
		"invalid tag":        {ExcludedTags: []string{"not-a-uuid"}},
		"duplicate tag":      {ExcludedTags: []string{tagID, tagID}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := p.Validate(); err == nil {
				t.Fatal("invalid filter policy accepted")
			}
		})
	}
	valid := filter.Policy{MediaTypes: []string{"image", "video"}, MaxOriginalBytes: 1, SkipRAW: true,
		IncludeArchived: new(false), ExcludedTags: []string{tagID}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid policy rejected: %v", err)
	}
}

func TestPolicyFingerprintTracksEffectiveRules(t *testing.T) {
	base := filter.Policy{}.Normalize()
	explicit := filter.Policy{MediaTypes: []string{"image"}, IncludeArchived: new(true)}.Normalize()
	if base.Fingerprint() != explicit.Fingerprint() {
		t.Fatal("equivalent policies have different fingerprints")
	}
	for name, changed := range map[string]filter.Policy{
		"video":     {MediaTypes: []string{"image", "video"}},
		"size":      {MaxOriginalBytes: 1},
		"raw":       {SkipRAW: true},
		"archive":   {IncludeArchived: new(false)},
		"tag order": {ExcludedTags: []string{tagID}},
	} {
		if changed.Normalize().Fingerprint() == base.Fingerprint() {
			t.Errorf("%s change did not alter the policy fingerprint", name)
		}
	}
	tagsA := filter.Policy{ExcludedTags: []string{"cccccccc-cccc-4ccc-8ccc-cccccccccccc", tagID}}.Normalize()
	tagsB := filter.Policy{ExcludedTags: []string{tagID, "cccccccc-cccc-4ccc-8ccc-cccccccccccc"}}.Normalize()
	if tagsA.Fingerprint() != tagsB.Fingerprint() {
		t.Fatal("tag order changed the policy fingerprint")
	}
}

func TestReasonsAreFinite(t *testing.T) {
	for _, reason := range []string{filter.MediaType, filter.RAW, filter.TooLarge, filter.SizeUnknown,
		filter.Archived, filter.Tag, filter.TagUnavailable} {
		if !filter.IsReason(reason) {
			t.Errorf("%q is not recognized", reason)
		}
	}
	if filter.IsReason("hidden") || filter.IsReason("") {
		t.Fatal("non-filter reason was recognized as a filter exclusion")
	}
}
