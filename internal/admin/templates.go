package admin

const pageTemplates = `
{{define "head"}}<meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><link rel="stylesheet" href="/admin.css">{{end}}
{{define "brand"}}<a class="brand" href="/">Immich <span>to</span> Amazon Photos</a>{{end}}
{{define "login"}}<!doctype html><html lang="en"><head>{{template "head"}}<title>Sign in | Immich to Amazon</title></head>
<body><main class="narrow">{{template "brand"}}<h1>Sign in</h1><p>Local control for your original-media backups. Use the passphrase from your mounted admin secret.</p>
<form class="stack" method="post" action="/login"><input type="hidden" name="csrf" value="{{.CSRF}}">
<label>Admin passphrase <input type="password" name="password" autocomplete="current-password" required autofocus></label><button>Sign in</button></form>
<p class="note">Keep this dashboard on localhost or a trusted private network.</p></main></body></html>{{end}}
{{define "index"}}<!doctype html><html lang="en"><head>{{template "head"}}<title>Backup control | Immich to Amazon</title></head>
<body><main><header>{{template "brand"}}<form method="post" action="/logout"><input type="hidden" name="csrf" value="{{.CSRF}}"><button class="quiet">Sign out</button></form></header>
<div class="page-title"><div><h1>Backup control</h1><p>Independent accounts. Original files. No cloud-media deletion.</p></div>{{if .Writable}}<a class="button" href="/accounts/new">Add account</a>{{end}}</div>
{{if not .Writable}}<p class="notice">Configuration is read-only. Start with an explicit config path and without --account to edit accounts. Account controls remain available.</p>{{end}}
{{range .Accounts}}<section class="account" aria-label="Account {{.Account.ID}}"><div class="account-heading"><h2>{{.Account.ID}}</h2><span class="state">{{if .Status.Paused}}Paused / {{.Status.PauseReason}}{{else if ne .Code "ok"}}{{.Code}}{{else}}Enabled{{end}}</span></div>
<p class="scope">Source: {{if .Account.Library}}whole accessible library{{else}}{{len .Account.AlbumIDs}} selected albums{{end}} <span>/</span> Destination: Amazon {{.Account.AmazonRegion}}</p>
{{if ne .Code "ok"}}<p role="status">Status unavailable: <code>{{.Code}}</code>. Check mounted credentials or run diagnosis.</p>{{end}}
<dl class="queue" aria-label="Media queue"><div><dt>Pending</dt><dd>{{.Status.Pending}}</dd></div><div><dt>Staged</dt><dd>{{.Status.Staged}}</dd></div><div><dt>Running</dt><dd>{{.Status.Running}}</dd></div><div><dt>Unconfirmed</dt><dd>{{.Status.Unconfirmed}}</dd></div><div><dt>Confirmed</dt><dd>{{.Status.Confirmed}}</dd></div><div><dt>Failed</dt><dd>{{.Status.Failed}}</dd></div><div><dt>Blocked</dt><dd>{{.Status.Blocked}}</dd></div><div><dt>Existing</dt><dd>{{.Status.Existing}}</dd></div><div><dt>Skipped</dt><dd>{{.Status.Skipped}}</dd></div></dl>
<dl class="diagnostics" aria-label="Event, album and association backlog"><dt>Events pending</dt><dd>{{.Status.EventsPending}}</dd><dt>Events failed</dt><dd>{{.Status.EventsFailed}}</dd><dt>Albums pending</dt><dd>{{.Status.AlbumsPending}}</dd><dt>Albums failed</dt><dd>{{.Status.AlbumsFailed}}</dd><dt>Albums unconfirmed</dt><dd>{{.Status.AlbumsUnconfirmed}}</dd><dt>Associations pending</dt><dd>{{.Status.AssociationsPending}}</dd><dt>Associations failed</dt><dd>{{.Status.AssociationsFailed}}</dd><dt>Associations unconfirmed</dt><dd>{{.Status.AssociationsUnconfirmed}}</dd></dl>
<p class="note">Work remaining: {{if .Status.WorkRemaining}}ready to run{{else}}none ready to run{{end}}. Incremental scan: {{if .Status.IncrementalScanning}}in progress{{else}}idle{{end}}. Last incremental reconcile: {{if .Status.LastIncremental}}<time datetime="{{.Status.LastIncremental.Format "2006-01-02T15:04:05Z07:00"}}">{{.Status.LastIncremental.Format "2006-01-02 15:04:05 MST"}}</time>{{else}}not yet completed{{end}}.</p>
<p class="note">Scopes scanning: {{.Status.Scanning}}. Last complete reconcile: {{if .Status.LastSuccess}}<time>{{.Status.LastSuccess.Format "2006-01-02 15:04:05 MST"}}</time>{{else}}not yet completed{{end}}.</p>
{{if .Status.Errors}}<details open><summary>Errors requiring attention</summary><dl class="diagnostics">{{range $code, $count := .Status.Errors}}<dt><code>{{$code}}</code></dt><dd>{{$count}}</dd>{{end}}</dl></details>{{end}}
<form class="actions" method="post"><input type="hidden" name="csrf" value="{{$.CSRF}}">
{{if .Status.Paused}}<button formaction="/accounts/{{.Account.ID}}/resume">Resume</button>{{else}}<button formaction="/accounts/{{.Account.ID}}/pause">Pause</button>{{end}}
<button class="quiet" formaction="/accounts/{{.Account.ID}}/retry">Retry eligible errors</button><button class="quiet" formaction="/accounts/{{.Account.ID}}/doctor">Run diagnosis</button>{{if $.Writable}}<a href="/accounts/{{.Account.ID}}/edit">Edit account</a>{{end}}
</form><p class="note">Retry preserves uncertain writes and manual pauses; it never blindly repeats an upload.</p>
{{if $.Writable}}<details><summary>Remove this account</summary><p>Stops scheduling this account. Local history and cloud media are preserved.</p><form method="post" action="/accounts/{{.Account.ID}}/remove"><input type="hidden" name="csrf" value="{{$.CSRF}}"><button class="danger">Remove account from configuration</button></form></details>{{end}}
</section>{{else}}<section class="empty"><h2>Your first backup starts here.</h2><p>Add an Immich source, choose an explicit album scope, and connect an Amazon Photos destination using mounted secret files.</p>{{if .Writable}}<a href="/accounts/new">Configure first account</a>{{end}}</section>{{end}}
<footer>Refresh this page for current durable queue counts. Remote failures do not change local readiness.</footer></main></body></html>{{end}}
{{define "editor"}}<!doctype html><html lang="en"><head>{{template "head"}}<title>Account | Immich to Amazon</title></head><body><main class="editor">
{{template "brand"}}<nav><a href="/">Back to overview</a></nav><h1>Account settings</h1><p>Source and destination credentials stay in separate mounted files. Enter paths, never secret values.</p>
<form class="stack" method="post" action="/accounts/save"><input type="hidden" name="csrf" value="{{.CSRF}}"><input type="hidden" name="original_id" value="{{.Original}}">
<label>Account ID <input name="id" value="{{.Account.ID}}" pattern="[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}" {{if .Original}}readonly{{end}} required aria-describedby="identity-note"></label><p class="note" id="identity-note">Stable, unique ID. Use a new ID to connect different cloud identities.</p>
<fieldset><legend>Source / Immich</legend><div class="stack">
<label>Server URL <input type="url" name="immich_url" value="{{.Account.ImmichURL}}" placeholder="https://photos.example.net" required></label>
<label>API key mounted file <input name="key_file" value="{{.Account.APIKeyFile}}" placeholder="/run/secrets/immich_key" required></label>
<div><button class="quiet" formaction="/accounts/discover" formnovalidate>List visible albums</button></div>
{{if .Albums}}<div class="album-list"><p>Add albums to the selection below:</p>{{range .Albums}}<label class="check"><input type="checkbox" name="album_id" value="{{.ID}}"><span>{{.Name}} <small>{{.Count}} assets</small></span></label>{{end}}</div>{{end}}
<label>Selected album IDs <textarea name="album_ids" rows="4" aria-describedby="scope-note">{{range .Account.AlbumIDs}}{{.}}
{{end}}</textarea></label><p class="note" id="scope-note">One ID per line, or comma-separated. Choose albums OR explicitly enable the whole library, never both.</p>
<label class="check"><input type="checkbox" name="library" {{if .Account.Library}}checked{{end}}><span>Explicitly sync the whole accessible library</span></label>
<label class="check"><input type="checkbox" name="include_videos" {{if .Account.IncludeVideos}}checked{{end}}><span>Include original videos</span></label>
</div></fieldset><fieldset><legend>Destination / Amazon Photos</legend><div class="stack">
<label>Region code <input name="region" value="{{.Account.AmazonRegion}}" placeholder="us" required aria-describedby="region-note"></label><p class="note" id="region-note">Use your actual account region; no region is selected automatically. Run diagnosis to inspect compatibility evidence.</p>
<label>Cookie mounted file <input name="cookie_file" value="{{.Account.CookieFile}}" placeholder="/run/secrets/amazon_cookie" required></label>
<label class="check"><input type="checkbox" name="mirror_albums" {{if .Account.MirrorAlbums}}checked{{end}}><span>Mirror managed album membership (requires selected albums; never delete cloud media)</span></label>
</div></fieldset><div class="actions"><button>Save account</button><a href="/">Cancel</a></div></form>
</main></body></html>{{end}}
{{define "doctor"}}<!doctype html><html lang="en"><head>{{template "head"}}<title>Diagnosis | Immich to Amazon</title></head><body><main class="editor">{{template "brand"}}<nav><a href="/">Back to overview</a></nav><h1>Account diagnosis</h1><p>{{.ID}} / <code>{{.Code}}</code></p><p class="notice">Read-only probes. Upload and album-write permissions are not tested.</p><dl class="diagnostics">{{range .Checks}}<dt>{{.Name}}</dt><dd><code>{{.Code}}</code></dd>{{end}}</dl></main></body></html>{{end}}
{{define "error"}}<!doctype html><html lang="en"><head>{{template "head"}}<title>Request not completed | Immich to Amazon</title></head><body><main class="narrow">{{template "brand"}}<div role="alert"><h1>Request not completed</h1><p><code>{{.}}</code></p><p>Review your settings and mounted secret files, then try again. No secret values are shown here.</p></div><a href="/">Back to overview</a></main></body></html>{{end}}
`
