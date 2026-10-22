package immich_test

import (
	"encoding/json"
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
			"originalPath":  "string",
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
