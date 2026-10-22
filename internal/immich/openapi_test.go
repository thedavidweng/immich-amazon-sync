package immich_test

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

type schema struct {
	Ref        string             `json:"$ref"`
	Type       string             `json:"type"`
	Nullable   bool               `json:"nullable"`
	Deprecated bool               `json:"deprecated"`
	Properties map[string]*schema `json:"properties"`
	Items      *schema            `json:"items"`
	Required   []string           `json:"required"`
	Enum       []string           `json:"enum"`
}

// The fixture is an extraction of official source, independent of our HTTP stubs.
func TestPinnedOfficialOpenAPIContract(t *testing.T) {
	data, err := os.ReadFile("testdata/immich-v3.3.4.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	type media map[string]struct{ Schema schema }
	var doc struct {
		Source struct{ Version, Commit, SHA256 string } `json:"x-source"`
		Paths  map[string]map[string]struct {
			Permission  string `json:"x-immich-permission"`
			Security    []map[string][]string
			Responses   map[string]struct{ Content media }
			RequestBody struct{ Content media }
		}
		Components struct {
			Schemas         map[string]schema
			SecuritySchemes map[string]struct{ Type, In, Name string }
		}
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Source.Version != "v3.3.4" || doc.Source.Commit != "69e0cb8606137c4f8dc0e7207a672ff8be39f358" ||
		doc.Source.SHA256 != "4fcfea46d57553d0c1bff3093fbc7257f81f250267f0c2368933497daaabcf3a" {
		t.Fatal("fixture must retain pinned upstream provenance")
	}
	key := doc.Components.SecuritySchemes["api_key"]
	if key.Type != "apiKey" || key.In != "header" || key.Name != "x-api-key" {
		t.Fatalf("API-key contract changed: %+v", key)
	}
	for _, tt := range []struct{ method, path, permission, response string }{
		{"get", "/server/version", "", "ServerVersionResponseDto"},
		{"get", "/users/me", "user.read", "UserAdminResponseDto"},
		{"get", "/albums", "album.read", ""},
		{"get", "/albums/{id}", "album.read", "AlbumResponseDto"},
		{"get", "/assets/{id}", "asset.read", "AssetResponseDto"},
		{"get", "/assets/{id}/original", "asset.download", ""},
		{"post", "/search/metadata", "asset.read", "SearchResponseDto"},
	} {
		op, ok := doc.Paths[tt.path][tt.method]
		if !ok || op.Permission != tt.permission {
			t.Fatalf("operation/permission changed: %s %s", tt.method, tt.path)
		}
		if tt.permission != "" && !slices.ContainsFunc(op.Security, func(s map[string][]string) bool {
			_, exists := s["api_key"]
			return exists
		}) {
			t.Fatalf("API-key access absent: %s", tt.path)
		}
		if tt.response != "" && op.Responses["200"].Content["application/json"].Schema.Ref != "#/components/schemas/"+tt.response {
			t.Fatalf("success schema changed: %s", tt.path)
		}
	}
	download := doc.Paths["/assets/{id}/original"]["get"].Responses["200"].Content["application/octet-stream"].Schema
	if download.Type != "string" {
		t.Fatal("original endpoint is no longer a binary response")
	}
	if doc.Paths["/search/metadata"]["post"].RequestBody.Content["application/json"].Schema.Ref != "#/components/schemas/MetadataSearchDto" {
		t.Fatal("metadata request schema changed")
	}
	for name, fields := range map[string]map[string]string{
		"ServerVersionResponseDto": {"major": "integer", "minor": "integer", "patch": "integer"},
		"UserAdminResponseDto":     {"id": "string"},
		"AlbumResponseDto":         {"id": "string", "albumName": "string", "assetCount": "integer", "shared": "boolean"},
		"MetadataSearchDto":        {"albumIds": "array", "page": "integer", "size": "integer", "withExif": "boolean", "updatedAfter": "string", "order": "string"},
		"SearchAssetResponseDto":   {"count": "integer", "items": "array", "nextPage": "string"},
		"AssetResponseDto": {
			"id": "string", "originalFileName": "string", "checksum": "string", "type": "string",
			"libraryId": "string", "visibility": "string", "isOffline": "boolean", "isTrashed": "boolean",
			"fileCreatedAt": "string", "updatedAt": "string", "livePhotoVideoId": "string",
		},
		"ExifResponseDto": {"fileSizeInByte": "integer"},
	} {
		for field, kind := range fields {
			prop := doc.Components.Schemas[name].Properties[field]
			if prop != nil && prop.Ref != "" {
				resolved := doc.Components.Schemas[strings.TrimPrefix(prop.Ref, "#/components/schemas/")]
				prop = &resolved
			}
			if prop == nil || prop.Type != kind {
				t.Fatalf("consumed field type changed: %s.%s must be %s", name, field, kind)
			}
		}
	}
	envelope := doc.Components.Schemas["SearchAssetResponseDto"]
	for _, field := range []string{"count", "items", "nextPage"} {
		if !slices.Contains(envelope.Required, field) {
			t.Fatalf("pagination evidence is not required: %s", field)
		}
	}
	if !envelope.Properties["nextPage"].Nullable || !envelope.Properties["nextPage"].Deprecated ||
		!doc.Components.Schemas["MetadataSearchDto"].Properties["albumIds"].Deprecated {
		t.Fatal("flat page contract support/deprecation changed; review before migrating")
	}
	if _, embedded := doc.Components.Schemas["AlbumResponseDto"].Properties["assets"]; embedded {
		t.Fatal("review album response change; membership must still use paged search")
	}
}

// Favorite, archive, tag, Live Photo and original-path semantics are pinned so a
// later slice can rely on presence instead of a zero value. The v3.3.4 DTO
// requires isFavorite on every asset response; tags and livePhotoVideoId stay
// optional, so their absence means unknown and must never be read as empty/false.
func TestPinnedAssetPresenceSemantics(t *testing.T) {
	data, err := os.ReadFile("testdata/immich-v3.3.4.openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Components struct {
			Schemas map[string]schema
		}
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	asset := doc.Components.Schemas["AssetResponseDto"]
	for _, field := range []string{"isFavorite", "isArchived", "originalPath", "visibility"} {
		if !slices.Contains(asset.Required, field) {
			t.Fatalf("presence-sensitive field is no longer required: %s", field)
		}
		if asset.Properties[field] == nil || asset.Properties[field].Nullable {
			t.Fatalf("presence-sensitive field changed nullability: %s", field)
		}
	}
	for _, field := range []string{"tags", "livePhotoVideoId"} {
		if slices.Contains(asset.Required, field) {
			t.Fatalf("optional presence evidence became required: %s", field)
		}
	}
	if !asset.Properties["livePhotoVideoId"].Nullable {
		t.Fatal("livePhotoVideoId must distinguish an explicit null companion from a missing field")
	}
	if asset.Properties["visibility"].Ref != "#/components/schemas/AssetVisibility" {
		t.Fatal("visibility no longer references the pinned enum")
	}
	if !slices.Contains(doc.Components.Schemas["AssetVisibility"].Enum, "archive") {
		t.Fatal("archive visibility is no longer part of the pinned contract")
	}
	if asset.Properties["tags"].Items == nil || asset.Properties["tags"].Items.Ref != "#/components/schemas/TagResponseDto" {
		t.Fatal("tags no longer carry pinned tag objects")
	}
	for _, field := range []string{"id", "name", "value", "createdAt", "updatedAt"} {
		if !slices.Contains(doc.Components.Schemas["TagResponseDto"].Required, field) {
			t.Fatalf("tag identity changed: %s is no longer required", field)
		}
	}
}

// The checked-in asset examples prove field presence is decidable. An asset
// without isFavorite is a contract violation on v3.3.4, not a "not favorite".
func TestPinnedAssetFieldPresence(t *testing.T) {
	data, err := os.ReadFile("testdata/immich-v3.3.4-assets.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Source struct{ Version, Commit, URL, SHA256 string } `json:"x-source"`
		Assets map[string]map[string]json.RawMessage         `json:"assets"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if doc.Source.Version != "v3.3.4" || doc.Source.Commit != "69e0cb8606137c4f8dc0e7207a672ff8be39f358" ||
		doc.Source.SHA256 != "4fcfea46d57553d0c1bff3093fbc7257f81f250267f0c2368933497daaabcf3a" {
		t.Fatal("asset fixture must retain pinned upstream provenance")
	}
	type expected struct {
		favorite, tags, livePhoto, libraryID string // "present", "absent", or "null"
		visibility, archived                 string
	}
	cases := map[string]expected{
		"favorite-true-archived": {favorite: "true", tags: "array", livePhoto: "null", libraryID: "null", visibility: "archive", archived: "true"},
		"favorite-false":         {favorite: "false", tags: "absent", livePhoto: "null", libraryID: "null", visibility: "timeline", archived: "false"},
		"favorite-absent":        {favorite: "absent", tags: "absent", livePhoto: "absent", libraryID: "null", visibility: "timeline", archived: "false"},
		"live-photo-still":       {favorite: "false", tags: "absent", livePhoto: "string", libraryID: "null", visibility: "timeline", archived: "false"},
		"live-photo-companion":   {favorite: "false", tags: "absent", livePhoto: "null", libraryID: "null", visibility: "hidden", archived: "false"},
		"external-library":       {favorite: "false", tags: "empty", livePhoto: "absent", libraryID: "string", visibility: "timeline", archived: "false"},
	}
	for name, want := range cases {
		fields, ok := doc.Assets[name]
		if !ok {
			t.Fatalf("missing pinned asset case: %s", name)
		}
		if got := presence(fields, "isFavorite"); got != want.favorite {
			t.Fatalf("%s.isFavorite presence = %s, want %s", name, got, want.favorite)
		}
		if got := presence(fields, "tags"); got != want.tags {
			t.Fatalf("%s.tags presence = %s, want %s", name, got, want.tags)
		}
		if got := presence(fields, "livePhotoVideoId"); got != want.livePhoto {
			t.Fatalf("%s.livePhotoVideoId presence = %s, want %s", name, got, want.livePhoto)
		}
		if got := presence(fields, "libraryId"); got != want.libraryID {
			t.Fatalf("%s.libraryId presence = %s, want %s", name, got, want.libraryID)
		}
		var visibility string
		if err := json.Unmarshal(fields["visibility"], &visibility); err != nil || visibility != want.visibility {
			t.Fatalf("%s.visibility = %q, %v", name, visibility, err)
		}
		var archived bool
		if err := json.Unmarshal(fields["isArchived"], &archived); err != nil || fmt.Sprint(archived) != want.archived {
			t.Fatalf("%s.isArchived = %v, %v", name, archived, err)
		}
		var originalPath string
		if err := json.Unmarshal(fields["originalPath"], &originalPath); err != nil || originalPath == "" {
			t.Fatalf("%s.originalPath missing: %q, %v", name, originalPath, err)
		}
	}
	// An absent boolean cannot be decoded the same way as an explicit false.
	var absent struct {
		Favorite *bool `json:"isFavorite"`
	}
	if err := json.Unmarshal(mustRaw(t, doc.Assets["favorite-absent"]), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.Favorite != nil {
		t.Fatal("absent isFavorite decoded as a value")
	}
	var explicit struct {
		Favorite *bool `json:"isFavorite"`
	}
	if err := json.Unmarshal(mustRaw(t, doc.Assets["favorite-false"]), &explicit); err != nil {
		t.Fatal(err)
	}
	if explicit.Favorite == nil || *explicit.Favorite {
		t.Fatal("explicit false isFavorite is not distinguishable from absence")
	}
}

func presence(fields map[string]json.RawMessage, key string) string {
	raw, ok := fields[key]
	if !ok {
		return "absent"
	}
	if string(raw) == "null" {
		return "null"
	}
	switch raw[0] {
	case '[':
		if string(raw) == "[]" {
			return "empty"
		}
		return "array"
	case '{':
		return "object"
	case 't', 'f':
		if string(raw) == "true" {
			return "true"
		}
		return "false"
	default:
		return "string"
	}
}

func mustRaw(t *testing.T, fields map[string]json.RawMessage) []byte {
	t.Helper()
	data, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
