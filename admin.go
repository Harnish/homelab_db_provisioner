package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

func backupOrDefault(b *BackupConfig) BackupConfig {
	if b == nil {
		return BackupConfig{Schedule: "daily"}
	}
	return *b
}

var adminTemplate = template.Must(template.New("admin").Funcs(template.FuncMap{
	"join":              strings.Join,
	"defaultSecretName": defaultSecretName,
	"backupOrDefault":   backupOrDefault,
	"when": func(rfc3339 string) string {
		t, err := time.Parse(time.RFC3339, rfc3339)
		if err != nil {
			return rfc3339
		}
		return t.Local().Format("2006-01-02 15:04")
	},
	"dbType": func(connStr string) string {
		switch detectDBType(connStr) {
		case MariaDB:
			return "mariadb"
		case MongoDB:
			return "mongodb"
		default:
			return "postgres"
		}
	},
}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>DB Provisioner Admin</title>
  <style>
    :root {
      --fg: #1c2024; --muted: #5b636b; --line: #d9dde1; --line-strong: #868f97;
      --bg: #fff; --panel: #f5f6f7; --hover: #eef0f2; --control: #fff;
      --ok: #1d6b3a; --ok-bg: #e3f3e8;
      --warn: #7a4b00; --warn-bg: #fdf1d8;
      --err: #9b1c1c; --err-bg: #fbe4e4; --err-line: #c46262;
      --info: #0b4f8a; --info-bg: #e5f0fb; --info-line: #9ec5fe;
      --s1: 0.25rem; --s2: 0.5rem; --s3: 0.75rem; --s4: 1rem; --s6: 1.5rem; --s8: 2rem;
      color-scheme: light dark;
      --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      /* Summary rows and the header row share these columns so they line up. */
      --cols: minmax(0, 1.3fr) minmax(0, 1fr) minmax(0, 1fr) minmax(0, 1.4fr) minmax(0, 1.3fr) 5rem;
    }
    /* Dark theme follows the OS setting. Surfaces get lighter as they rise
       (canvas < panel < control); semantic hues are lifted so they keep their
       meaning at night without glare. Every text pair stays at or above 4.5:1,
       every control border at or above 3:1. */
    @media (prefers-color-scheme: dark) {
      :root {
        --fg: #e3e6e9; --muted: #9aa3ab; --line: #2f353b; --line-strong: #78818a;
        --bg: #15181b; --panel: #1d2125; --hover: #2e343a; --control: #252a2f;
        --ok: #74d196; --ok-bg: #16342a;
        --warn: #f0bd5c; --warn-bg: #3a2c0e;
        --err: #f49a9a; --err-bg: #3e1d1d; --err-line: #b35f5f;
        --info: #84bdf5; --info-bg: #14304c; --info-line: #3b6c9c;
      }
    }
    * { box-sizing: border-box; }
    body { font: 15px/1.45 system-ui, sans-serif; color: var(--fg); background: var(--bg); max-width: 1280px; margin: var(--s8) auto; padding: 0 var(--s4); }
    ::selection { background: var(--info-bg); color: var(--fg); }
    h1 { font-size: 1.5rem; margin: 0 0 var(--s2); }
    h2 { font-size: 1.15rem; margin: 0 0 var(--s4); }
    h2:not(:first-child) { margin-top: var(--s8); }
    h3 { font-size: 1.05rem; margin: 0; }
    h4 { font-size: 0.875rem; margin: 0 0 var(--s2); }
    p { margin: 0 0 var(--s2); }
    code, .mono { font-family: var(--mono); font-size: 0.875em; }
    pre { font-family: var(--mono); font-size: 0.8125rem; background: var(--panel); padding: var(--s3); border-radius: 4px; overflow-x: auto; }
    .muted { color: var(--muted); }
    .hint { font-size: 0.8125rem; color: var(--muted); }
    .vh { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }

    .mode { color: var(--muted); margin-bottom: var(--s6); }
    .badge { display: inline-block; font-size: 0.8125rem; line-height: 1.5; padding: 0 var(--s2); border-radius: 0.75em; background: var(--panel); color: var(--muted); }
    .badge.ok { background: var(--ok-bg); color: var(--ok); }
    .badge.warn { background: var(--warn-bg); color: var(--warn); }
    .badge.err { background: var(--err-bg); color: var(--err); }
    .badge.info { background: var(--info-bg); color: var(--info); }

    .flash, .reveal { padding: var(--s3) var(--s4); border-radius: 4px; margin-bottom: var(--s4); }
    .flash-ok { background: var(--ok-bg); color: var(--ok); }
    .flash-err { background: var(--err-bg); color: var(--err); }
    .reveal { background: var(--info-bg); color: var(--info); }
    .reveal .secret { display: inline-block; font-size: 1.05rem; padding: var(--s1) var(--s2); margin-bottom: var(--s2); background: var(--control); color: var(--fg); border: 1px solid var(--info-line); border-radius: 4px; user-select: all; overflow-wrap: anywhere; }
    .error { color: var(--err); overflow-wrap: anywhere; }

    .layout { display: flex; flex-wrap: wrap; gap: var(--s8); align-items: flex-start; }
    .col-left { flex: 3 1 44rem; min-width: 0; }
    .col-right { flex: 1 1 280px; }

    .server { margin-bottom: var(--s8); }
    .server-head { display: flex; flex-wrap: wrap; align-items: baseline; gap: var(--s2); }
    .attention { font-size: 0.875rem; margin: var(--s1) 0 var(--s3); color: var(--muted); }
    .attention.ok { color: var(--ok); }
    .attention.warn { color: var(--warn); font-weight: 600; }

    .db-cols, .db > summary { display: grid; grid-template-columns: var(--cols); gap: var(--s3); padding: var(--s2) var(--s3); }
    .db-cols { font-size: 0.75rem; font-weight: 600; color: var(--muted); border-bottom: 1px solid var(--line-strong); }
    .db-cols button { all: unset; cursor: pointer; }
    .db-cols button:hover { color: var(--fg); }
    .db-cols button:focus-visible { outline: 2px solid var(--info); outline-offset: 1px; }
    .db-cols button[data-dir=asc]::after { content: " \2191"; }
    .db-cols button[data-dir=desc]::after { content: " \2193"; }
    .db-filter { margin-bottom: var(--s2); }
    .db { border-bottom: 1px solid var(--line); }
    .db > summary { cursor: pointer; list-style: none; align-items: baseline; min-height: 2.75rem; overflow-wrap: anywhere; }
    .db > summary::-webkit-details-marker { display: none; }
    .db > summary:hover, .db[open] > summary { background: var(--panel); }
    .db > summary:focus-visible, .sub > summary:focus-visible { outline: 2px solid var(--info); outline-offset: -2px; }
    .sub-line { display: block; font-size: 0.8125rem; color: var(--muted); }
    .sub-line.warn { color: var(--warn); }
    .lbl { position: absolute; width: 1px; height: 1px; overflow: hidden; clip-path: inset(50%); white-space: nowrap; }
    .manage { justify-self: end; font-size: 0.875rem; color: var(--info); white-space: nowrap; }
    .manage::after { content: ""; display: inline-block; width: 0.4em; height: 0.4em; margin-left: 0.4em; border: solid currentColor; border-width: 0 1.5px 1.5px 0; transform: translateY(-0.2em) rotate(45deg); transition: transform 0.15s ease-out; }
    .db[open] .manage::after { transform: translateY(0.05em) rotate(-135deg); }

    .panel { background: var(--panel); padding: var(--s2) var(--s3) var(--s6); display: grid; grid-template-columns: repeat(auto-fit, minmax(15rem, 1fr)); gap: var(--s4) var(--s8); }
    .group > form, .group > .row { margin-bottom: var(--s3); }
    .row { display: flex; flex-wrap: wrap; align-items: center; gap: var(--s2); }
    .sub > summary { cursor: pointer; color: var(--info); font-size: 0.875rem; padding: var(--s1) 0; }
    .sub[open] > summary { margin-bottom: var(--s2); }

    label { display: block; margin-bottom: var(--s3); }
    .row label, label.check { display: flex; align-items: center; gap: var(--s2); margin: 0; }
    label.check { align-items: flex-start; }
    label.check input { margin-top: 0.15em; flex: none; }
    label.check { margin-bottom: var(--s2); }
    input[type=text], input[type=search], input[type=password], input[type=number], select { font: inherit; min-height: 2rem; padding: 0 var(--s2); border: 1px solid var(--line-strong); border-radius: 4px; background: var(--control); color: var(--fg); }
    input[type=text], input[type=search], input[type=password], select { width: 100%; max-width: 300px; }
    .row select { width: auto; }
    .row input[type=password] { width: 12rem; }
    input[type=number] { width: 5rem; }
    input[type=checkbox] { width: 1.1rem; height: 1.1rem; margin: 0; accent-color: var(--info); }
    input:focus-visible, select:focus-visible, button:focus-visible { outline: 2px solid var(--info); outline-offset: 1px; }
    button { font: inherit; font-size: 0.875rem; min-height: 2rem; padding: 0 var(--s3); border: 1px solid var(--line-strong); border-radius: 4px; background: var(--control); color: var(--fg); cursor: pointer; }
    button:hover { background: var(--hover); }
    button.danger { color: var(--err); border-color: var(--err-line); }
    button.danger:hover { background: var(--err-bg); }

    .col-right form { border: 1px solid var(--line); border-radius: 6px; padding: var(--s4); margin-bottom: var(--s8); }

    @media (max-width: 720px) {
      .db-cols { display: none; }
      .db > summary { grid-template-columns: 1fr auto; row-gap: var(--s1); }
      .db > summary > :not(.c-name):not(.manage) { grid-column: 1 / -1; }
      .db > summary > .manage { grid-row: 1; grid-column: 2; }
      .lbl { position: static; width: auto; height: auto; overflow: visible; clip-path: none; color: var(--muted); font-size: 0.8125rem; }
    }
  </style>
</head>
<body>
  <main>
  <h1>DB Provisioner Admin</h1>
  <p class="mode">
    {{if .WatchMode}}<span class="badge ok">Watch mode on</span> Changes saved here reach your databases within about 10 seconds.
    {{else}}<span class="badge warn">Watch mode off</span> Changes are saved to the config file but won't reach your databases until the provisioner runs again.{{end}}
    {{if .K8sEnabled}}New databases get their password in a Kubernetes Secret in namespace <code>{{.Namespace}}</code>. Move existing ones under Manage.{{end}}
  </p>
  {{if .Flash}}
    <div class="flash {{if .FlashError}}flash-err{{else}}flash-ok{{end}}" role="{{if .FlashError}}alert{{else}}status{{end}}">{{.Flash}}</div>
  {{end}}
  {{with .Reveal}}
    <div class="reveal" role="status">
      <p>Password for user <strong>{{.User}}</strong> on database <strong>{{.Database}}</strong> ({{.Server}}):</p>
      <code class="secret">{{.Password}}</code>
      <p><small>Only this response contains the password; it isn't in the URL or saved by the browser. <a href="/">Hide it</a></small></p>
    </div>
  {{end}}

  <div class="layout">
  <div class="col-left">
  <h2>Servers</h2>
  {{range $si, $server := .Servers}}
    {{$health := index $.Health $si}}
    {{$isPG := eq (dbType $server.RootConnectionString) "postgres"}}
    <section class="server" aria-labelledby="server-{{$si}}">
      <div class="server-head">
        <h3 id="server-{{$si}}">{{$server.Name}}</h3>
        <span class="badge">{{dbType $server.RootConnectionString}}</span>
        {{if $server.DryRun}}<span class="badge warn">Dry run: SQL is logged, not executed</span>{{end}}
      </div>
      {{if not $server.Databases}}
      <p class="attention">No databases on this server yet.{{if $isPG}} It can still be a migration target.{{end}}</p>
      {{else}}
      {{with $health.Items}}<p class="attention {{if $health.Warn}}warn{{end}}">{{join . " · "}}</p>
      {{else}}<p class="attention ok">{{if eq (len $server.Databases) 1}}Database{{else}}All {{len $server.Databases}} databases{{end}} OK</p>{{end}}

      <input type="search" class="db-filter" placeholder="Filter databases" aria-label="Filter databases on {{$server.Name}}">
      <div class="db-cols"><button type="button">Database</button><button type="button">User</button><button type="button">Access</button><button type="button">Backup</button><button type="button">Migration</button><span></span></div>
      <div class="db-list">
      {{range $di, $db := $server.Databases}}
      {{$h := index $health.DBs $di}}
      {{$b := backupOrDefault $db.Backup}}
      {{$tomb := and $db.Migrate $db.Migrate.Completed}}
      <details class="db" id="db-{{$si}}-{{$di}}">
        <summary>
          <span class="c-name mono">{{$db.Database}}</span>
          <span><span class="lbl">User</span> <span class="mono">{{$db.User}}</span></span>
          <span><span class="lbl">Access</span> {{if $db.Permissions}}{{join $db.Permissions ", "}}{{else}}ALL{{end}}
            {{if $db.Extensions}}<span class="sub-line">+ {{join $db.Extensions ", "}}</span>{{end}}</span>
          <span><span class="lbl">Backup</span>
            {{if $tomb}}<span class="muted">&mdash;<span class="vh">not managed here</span></span>
            {{else if eq $h.Backup "off"}}<span class="muted">Off</span>
            {{else}}{{if eq $b.Schedule "weekly"}}Weekly{{else}}Daily{{end}}, keep {{if $b.KeepCount}}{{$b.KeepCount}}{{else}}all{{end}}
              {{if eq $h.Backup "none"}}<span class="sub-line warn">No backup on disk yet</span>
              {{else if eq $h.Backup "overdue"}}<span class="sub-line warn">Overdue, last {{$h.LastBackup}}</span>
              {{else}}<span class="sub-line">Last {{$h.LastBackup}}</span>{{end}}
            {{end}}</span>
          <span><span class="lbl">Migration</span>
            {{if not $db.Migrate}}<span class="muted">&mdash;<span class="vh">none</span></span>
            {{else if $tomb}}<span class="badge info">Migrated</span><span class="sub-line">to {{$db.Migrate.TargetServer}}</span>
            {{else if $db.Migrate.Error}}<span class="badge err">Failed</span><span class="sub-line">to {{$db.Migrate.TargetServer}}, retrying</span>
            {{else}}<span class="badge warn">Pending</span><span class="sub-line">to {{$db.Migrate.TargetServer}}</span>{{end}}</span>
          <span class="manage">Manage</span>
        </summary>

        <div class="panel">
        {{if $tomb}}
          <section class="group">
            <h4>Migration</h4>
            <p>This database was migrated to <strong>{{$db.Migrate.TargetServer}}</strong> on {{when $db.Migrate.CompletedAt}}. The provisioner and backups now skip this entry on {{$server.Name}}.</p>
            {{if $db.Migrate.Error}}<p class="error">{{$db.Migrate.Error}}</p>{{end}}
            <p class="hint">Next step: remove this entry from the config, or move it under {{$db.Migrate.TargetServer}}. Clearing the record makes the provisioner manage it on {{$server.Name}} again{{if $db.Migrate.ConfirmDrop}} and recreate it there empty, since the source was dropped{{end}}.</p>
            <form method="POST" action="/migrate-database" onsubmit="return confirm('Clear the migration record for {{$db.Database}}? The provisioner will manage it on {{$server.Name}} again{{if $db.Migrate.ConfirmDrop}} and recreate it there empty, since the source was dropped{{end}}. Remove or move the entry instead if it now lives on {{$db.Migrate.TargetServer}}.')">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
              <input type="hidden" name="target_server" value="">
              <button type="submit">Clear migration record</button>
            </form>
          </section>
        {{else}}
          <section class="group">
          {{if $db.K8sSecret}}
            {{$sn := $db.K8sSecret}}
            <h4>Kubernetes Secret</h4>
            <p><code>{{$sn}}</code>{{if $db.RequiresConnectString}} <span class="hint">+ connection_string</span>{{end}}</p>
            {{if $.K8sEnabled}}
            <p class="hint">Reference it from your Deployment:</p>
            <pre>env:
- name: DB_PASSWORD
  valueFrom:
    secretKeyRef:
      name: {{$sn}}
      key: password{{if $db.RequiresConnectString}}
- name: DATABASE_URL
  valueFrom:
    secretKeyRef:
      name: {{$sn}}
      key: connection_string{{end}}</pre>
            <p class="hint">Read it:</p>
            <pre>kubectl get secret {{$sn}} -n {{$.Namespace}} -o jsonpath='{.data.password}' | base64 -d</pre>
            <form method="POST" action="/rotate-secret" onsubmit="return confirm('Rotate the secret for {{$db.Database}}? Apps using the old password will fail until they reload the secret.')">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
              <button type="submit">Rotate secret</button>
            </form>
            {{else}}
            <p class="error">USE_KUBERNETES_SECRETS is off, so the provisioner can't read this password and skips this database.</p>
            {{end}}
          {{else}}
            <h4>Password</h4>
            <div class="row">
              <form method="POST" action="/reveal-password">
                <input type="hidden" name="server_index" value="{{$si}}">
                <input type="hidden" name="db_index" value="{{$di}}">
                <input type="hidden" name="server" value="{{$server.Name}}">
                <input type="hidden" name="database" value="{{$db.Database}}">
                <button type="submit">Show password</button>
              </form>
              <form method="POST" action="/generate-password" onsubmit="return confirm('Replace the password for {{$db.Database}} with a generated one? Apps using the old password will fail until they are updated.')">
                <input type="hidden" name="server_index" value="{{$si}}">
                <input type="hidden" name="db_index" value="{{$di}}">
                <input type="hidden" name="server" value="{{$server.Name}}">
                <input type="hidden" name="database" value="{{$db.Database}}">
                <button type="submit">Generate new</button>
              </form>
            </div>
            <form method="POST" action="/update-password" class="row" onsubmit="return confirm('Replace the password for {{$db.Database}}? Apps using the old password will fail until they are updated.')">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
              <label>Set to<span class="vh"> new password for {{$db.Database}}</span> <input type="password" name="new_password" autocomplete="new-password" required></label>
              <button type="submit">Save password</button>
            </form>
            {{if $.K8sEnabled}}
            <form method="POST" action="/move-to-secret" class="row" onsubmit="return confirm('Move the password for {{$db.Database}} into a Kubernetes Secret? The password stays the same and is removed from the config file.')">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
              <label>Secret<span class="vh"> name for {{$db.Database}}</span> <input type="text" name="secret_name" value="{{defaultSecretName $db.Database}}" autocomplete="off" spellcheck="false" required></label>
              <button type="submit">Move to Kubernetes Secret</button>
            </form>
            {{end}}
          {{end}}
          </section>

          <section class="group">
            <h4>Backups</h4>
            <form method="POST" action="/update-backup">
              <input type="hidden" name="server_index" value="{{$si}}">
              <input type="hidden" name="db_index" value="{{$di}}">
              <input type="hidden" name="server" value="{{$server.Name}}">
              <input type="hidden" name="database" value="{{$db.Database}}">
              <label class="check"><input type="checkbox" name="backup_enabled" {{if $b.Enabled}}checked{{end}}> Back up {{$db.Database}}</label>
              <div class="row">
                <select name="backup_schedule" aria-label="Backup schedule for {{$db.Database}}">
                  <option value="daily" {{if eq $b.Schedule "daily"}}selected{{end}}>Daily</option>
                  <option value="weekly" {{if eq $b.Schedule "weekly"}}selected{{end}}>Weekly (Sundays)</option>
                </select>
                <label>keep <input type="number" name="backup_keep_count" value="{{$b.KeepCount}}" min="0" aria-describedby="keep-hint-{{$si}}-{{$di}}"></label>
              </div>
              <p class="hint" id="keep-hint-{{$si}}-{{$di}}">Keep 0 keeps every backup.</p>
              <label class="check"><input type="checkbox" name="backup_restore_on_create" {{if $b.RestoreOnCreate}}checked{{end}}> Restore the newest backup if this database is recreated</label>
              <button type="submit">Save backup settings</button>
            </form>
          </section>

          {{if $isPG}}
          <section class="group">
            <h4>Migration</h4>
            {{if not $db.Migrate}}
              {{if gt (len $.PGServerNames) 1}}
              <p class="hint">Copy this database to another PostgreSQL server, e.g. for a major-version upgrade.</p>
              <details class="sub">
                <summary>Migrate {{$db.Database}} to another server</summary>
                <form method="POST" action="/migrate-database">
                  <input type="hidden" name="server_index" value="{{$si}}">
                  <input type="hidden" name="db_index" value="{{$di}}">
                  <input type="hidden" name="server" value="{{$server.Name}}">
                  <input type="hidden" name="database" value="{{$db.Database}}">
                  <label>Target server
                    <select name="target_server" required>
                      <option value="">Choose a server</option>
                      {{range $.PGServerNames}}{{if ne . $server.Name}}<option value="{{.}}">{{.}}</option>{{end}}{{end}}
                    </select>
                  </label>
                  <label>Drop the source after a verified copy (optional). Type <code>{{$db.Database}}</code> to confirm; leave empty to keep it.
                    <input type="text" name="confirm_database" autocomplete="off" spellcheck="false">
                  </label>
                  <button type="submit">Schedule migration</button>
                </form>
              </details>
              {{else}}
              <p class="hint">Add another PostgreSQL server to migrate this database to it.</p>
              {{end}}
            {{else}}
              {{if $db.Migrate.Error}}
              <p>Migration to <strong>{{$db.Migrate.TargetServer}}</strong> failed. The provisioner retries it on every pass.</p>
              <p class="error">{{$db.Migrate.Error}}</p>
              {{else}}
              <p>Waiting for the provisioner to migrate this database to <strong>{{$db.Migrate.TargetServer}}</strong>{{if $db.Migrate.ConfirmDrop}}. The source will be dropped once the copy is verified{{end}}.</p>
              {{end}}
              <form method="POST" action="/migrate-database">
                <input type="hidden" name="server_index" value="{{$si}}">
                <input type="hidden" name="db_index" value="{{$di}}">
                <input type="hidden" name="server" value="{{$server.Name}}">
                <input type="hidden" name="database" value="{{$db.Database}}">
                <input type="hidden" name="target_server" value="">
                <button type="submit">Cancel migration</button>
              </form>
            {{end}}
          </section>
          {{end}}
        {{end}}
        </div>
      </details>
      {{end}}
      </div>
      {{end}}
    </section>
  {{else}}
    <p class="muted">No servers configured yet. Add one with the Add Server form.</p>
  {{end}}

  </div>

  <div class="col-right">
  <h2>Add Database</h2>
  {{if not .Servers}}
  <p class="muted">Add a server first, then add databases to it.</p>
  {{else}}
  <form method="POST" action="/add-database">
    <label>Server
      <select name="server_index">
        {{range $si, $server := .Servers}}
        <option value="{{$si}}">{{$server.Name}}</option>
        {{end}}
      </select>
    </label>
    <label>Database name <input type="text" name="database" required></label>
    <label>Username <input type="text" name="user" required></label>
    {{if .K8sEnabled}}<label>Kubernetes Secret name (blank for <code>&lt;database&gt;-credentials</code>) <input type="text" name="secret_name" autocomplete="off" spellcheck="false"></label>
    <p class="hint">A password is generated into this Secret.</p>
    {{else}}<label>Password (blank to generate one) <input type="password" name="password" autocomplete="new-password"></label>{{end}}
    <label>Permissions (comma-separated, blank for ALL) <input type="text" name="permissions"></label>
    <label class="check"><input type="checkbox" name="requires_connect_string"> Store full connection_string in Kubernetes secret{{if not .K8sEnabled}} (requires USE_KUBERNETES_SECRETS){{end}}</label>
    <label class="check"><input type="checkbox" name="backup_enabled"> Back up this database</label>
    <div class="row">
      <select name="backup_schedule" aria-label="Backup schedule">
        <option value="daily">Daily</option>
        <option value="weekly">Weekly (Sundays)</option>
      </select>
      <label>keep <input type="number" name="backup_keep_count" value="0" min="0" aria-describedby="add-keep-hint"></label>
    </div>
    <p class="hint" id="add-keep-hint">Keep 0 keeps every backup.</p>
    <label class="check"><input type="checkbox" name="backup_restore_on_create"> Restore the newest backup when the database is created</label>
    <button type="submit">Add Database</button>
  </form>
  {{end}}

  <h2>Add Server</h2>
  <form method="POST" action="/add-server">
    <label>Name <input type="text" name="name" required></label>
    <label>Root connection string <input type="text" name="root_connection_string" placeholder="postgres://user:pass@host:5432/postgres" autocomplete="off" spellcheck="false" required></label>
    <label class="check"><input type="checkbox" name="dry_run"> Dry run (log SQL, don't execute it)</label>
    <button type="submit">Add Server</button>
  </form>
  </div>
  </div>
  </main>
  <script>
    // Client-side sort (header click) and filter for each server's database list.
    document.querySelectorAll('.server').forEach(function (s) {
      var list = s.querySelector('.db-list');
      if (!list) return;
      var cell = function (d, i) { return d.querySelector('summary').children[i].textContent.replace(/\s+/g, ' ').trim(); };
      s.querySelector('.db-filter').addEventListener('input', function (e) {
        var q = e.target.value.trim().toLowerCase();
        Array.from(list.children).forEach(function (d) {
          d.hidden = q !== '' && !d.querySelector('summary').textContent.toLowerCase().includes(q);
        });
      });
      var btns = s.querySelectorAll('.db-cols button');
      btns.forEach(function (b, i) {
        b.addEventListener('click', function () {
          var dir = b.dataset.dir === 'asc' ? 'desc' : 'asc';
          btns.forEach(function (o) { delete o.dataset.dir; });
          b.dataset.dir = dir;
          Array.from(list.children)
            .sort(function (x, y) {
              var c = cell(x, i).localeCompare(cell(y, i), undefined, { numeric: true, sensitivity: 'base' });
              return dir === 'asc' ? c : -c;
            })
            .forEach(function (d) { list.appendChild(d); });
        });
      });
    });
  </script>
</body>
</html>`))

type adminTemplateData struct {
	Servers    []DatabaseServer
	Flash      string
	FlashError bool
	K8sEnabled bool
	Namespace  string
	WatchMode  bool
	// Reveal is set only on a /reveal-password response.
	Reveal *revealedPassword
	// PGServerNames lists the names of all PostgreSQL servers, offered as
	// migration targets in the admin UI.
	PGServerNames []string
	// Health is parallel to Servers: the read-only status shown for each.
	Health []serverHealth
}

// dbHealth is the backup status shown for one database.
type dbHealth struct {
	Backup     string // "off", "none" (enabled, nothing on disk yet), "ok", or "overdue"
	LastBackup string // "today", "yesterday", "3 days ago"; empty unless Backup is ok/overdue
}

type serverHealth struct {
	Items []string // short status notes for the server; empty means all OK
	Warn  bool     // some item needs the operator's attention
	DBs   []dbHealth
}

// healthFor derives each server's status from the config and the local
// backups directory. S3 is not consulted: the page renders on every load and
// shouldn't wait on the network.
func healthFor(configPath string, cfg *Config) []serverHealth {
	today := time.Now()
	today = time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.Local)
	out := make([]serverHealth, len(cfg.Servers))
	for si, s := range cfg.Servers {
		var failed, overdue, none, pending, migrated int
		sh := &out[si]
		for _, db := range s.Databases {
			h := dbHealth{Backup: "off"}
			m := db.Migrate
			switch {
			case m != nil && m.Completed:
				migrated++
			case m != nil && m.Error != "":
				failed++
			case m != nil:
				pending++
			}
			if db.Backup != nil && db.Backup.Enabled && (m == nil || !m.Completed) {
				h.Backup, h.LastBackup = backupAge(configPath, s.Name, db, today)
				switch h.Backup {
				case "overdue":
					overdue++
				case "none":
					none++
				}
			}
			sh.DBs = append(sh.DBs, h)
		}
		note := func(n int, one, many string, warn bool) {
			switch {
			case n == 1:
				sh.Items = append(sh.Items, "1 "+one)
			case n > 1:
				sh.Items = append(sh.Items, fmt.Sprintf("%d %s", n, many))
			default:
				return
			}
			sh.Warn = sh.Warn || warn
		}
		note(failed, "migration failed", "migrations failed", true)
		note(overdue, "backup overdue", "backups overdue", true)
		note(none, "database with no backup yet", "databases with no backup yet", true)
		note(pending, "migration pending", "migrations pending", false)
		note(migrated, "migrated entry to clean up", "migrated entries to clean up", false)
	}
	return out
}

// backupAge finds the newest local backup ({database}_{YYYY-MM-DD}.*.gz, as
// written by runBackups) and whether it is older than the schedule allows.
func backupAge(configPath, server string, db DatabaseConfig, today time.Time) (state, age string) {
	dir := filepath.Join(filepath.Dir(configPath), "backups", slugify(server), db.Database)
	files, _ := filepath.Glob(filepath.Join(dir, db.Database+"_*.gz"))
	if len(files) == 0 {
		return "none", ""
	}
	sort.Strings(files)
	stamp := strings.TrimPrefix(filepath.Base(files[len(files)-1]), db.Database+"_")
	if len(stamp) < 10 {
		return "none", ""
	}
	d, err := time.ParseInLocation("2006-01-02", stamp[:10], time.Local)
	if err != nil {
		return "none", ""
	}
	days := int(today.Sub(d).Hours() / 24)
	switch days {
	case 0:
		age = "today"
	case 1:
		age = "yesterday"
	default:
		age = fmt.Sprintf("%d days ago", days)
	}
	// Backups run at midnight; weekly ones only on Sundays.
	limit := 1
	if db.Backup.Schedule == "weekly" {
		limit = 7
	}
	if days > limit {
		return "overdue", age
	}
	return "ok", age
}

func newAdminHandler(configPath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/", handleIndex(configPath))
	mux.HandleFunc("/update-password", handleUpdatePassword(configPath))
	mux.HandleFunc("/generate-password", handleGeneratePassword(configPath))
	mux.HandleFunc("/reveal-password", handleRevealPassword(configPath))
	mux.HandleFunc("/rotate-secret", handleRotateSecret(configPath))
	mux.HandleFunc("/move-to-secret", handleMoveToSecret(configPath))
	mux.HandleFunc("/update-backup", handleUpdateBackup(configPath))
	mux.HandleFunc("/add-database", handleAddDatabase(configPath))
	mux.HandleFunc("/add-server", handleAddServer(configPath))
	mux.HandleFunc("/migrate-database", handleMigrateDatabase(configPath))
	mux.HandleFunc("GET /api/servers", handleAPIListServers(configPath))
	mux.HandleFunc("GET /api/servers/{si}", handleAPIGetServer(configPath))
	mux.HandleFunc("POST /api/servers", handleAPICreateServer(configPath))
	mux.HandleFunc("PATCH /api/servers/{si}", handleAPIUpdateServer(configPath))
	mux.HandleFunc("DELETE /api/servers/{si}", handleAPIDeleteServer(configPath))
	mux.HandleFunc("GET /api/servers/{si}/databases", handleAPIListDatabases(configPath))
	mux.HandleFunc("GET /api/servers/{si}/databases/{di}", handleAPIGetDatabase(configPath))
	mux.HandleFunc("POST /api/servers/{si}/databases", handleAPICreateDatabase(configPath))
	mux.HandleFunc("PATCH /api/servers/{si}/databases/{di}", handleAPIUpdateDatabase(configPath))
	mux.HandleFunc("DELETE /api/servers/{si}/databases/{di}", handleAPIDeleteDatabase(configPath))

	// Browsers replay cached Basic Auth credentials on cross-site form posts,
	// so reject cross-origin writes (CSRF). Non-browser API clients send no
	// Sec-Fetch-Site/Origin header and are unaffected.
	protected := http.NewCrossOriginProtection().Handler(basicAuth(mux))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Refuse framing so a hostile page can't clickjack the action buttons.
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Content-Security-Policy", "frame-ancestors 'none'")
		// Answer favicon.ico without an auth challenge. A 401 on a path the
		// browser fetches on its own (favicon) pops a second Basic Auth
		// dialog on top of the one for "/".
		if r.URL.Path == "/favicon.ico" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		protected.ServeHTTP(w, r)
	})
}

func startAdminServer(configPath string) {
	port := os.Getenv("ADMIN_PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Admin server listening on :%s", port)
	if err := http.ListenAndServe(":"+port, newAdminHandler(configPath)); err != nil {
		log.Fatalf("Admin server error: %v", err)
	}
}

func basicAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		adminUser := os.Getenv("ADMIN_USER")
		adminPass := os.Getenv("ADMIN_PASSWORD")
		userOK := subtle.ConstantTimeCompare([]byte(user), []byte(adminUser)) == 1
		passOK := subtle.ConstantTimeCompare([]byte(pass), []byte(adminPass)) == 1
		if !ok || !userOK || !passOK {
			w.Header().Set("WWW-Authenticate", `Basic realm="Admin"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func handleIndex(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		configMu.RLock()
		data, err := os.ReadFile(configPath)
		configMu.RUnlock()
		if err != nil {
			http.Error(w, "Failed to read config file "+configPath+": "+err.Error(), http.StatusInternalServerError)
			return
		}
		var cfg Config
		if err := json.Unmarshal(data, &cfg); err != nil {
			http.Error(w, "Config file "+configPath+" is not valid JSON: "+err.Error()+"\nFix the file and reload this page.", http.StatusInternalServerError)
			return
		}
		renderIndex(w, configPath, &cfg, r.URL.Query().Get("msg"), nil)
	}
}

type revealedPassword struct {
	Server, Database, User, Password string
}

func renderIndex(w http.ResponseWriter, configPath string, cfg *Config, msg string, reveal *revealedPassword) {
	tmplData := adminTemplateData{
		Servers:    cfg.Servers,
		Health:     healthFor(configPath, cfg),
		Flash:      msg,
		FlashError: strings.HasPrefix(msg, "Error:"),
		K8sEnabled: secretsManager != nil,
		WatchMode:  os.Getenv("WATCH_MODE") == "true",
		Reveal:     reveal,
	}
	if secretsManager != nil {
		tmplData.Namespace = secretsManager.namespace
	}
	for _, s := range cfg.Servers {
		if detectDBType(s.RootConnectionString) == PostgreSQL {
			tmplData.PGServerNames = append(tmplData.PGServerNames, s.Name)
		}
	}
	if err := adminTemplate.Execute(w, tmplData); err != nil {
		log.Printf("template execute error: %v", err)
	}
}

// handleRevealPassword shows one database's password when Kubernetes Secret
// mode is off (the config file is then the only place it lives). The password
// is only ever in this POST response body: never in a URL or flash message,
// and never cached.
func handleRevealPassword(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		si, err := strconv.Atoi(r.FormValue("server_index"))
		if err != nil {
			http.Error(w, "Invalid server_index", http.StatusBadRequest)
			return
		}
		di, err := strconv.Atoi(r.FormValue("db_index"))
		if err != nil {
			http.Error(w, "Invalid db_index", http.StatusBadRequest)
			return
		}

		configMu.RLock()
		fileData, err := os.ReadFile(configPath)
		configMu.RUnlock()
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
			return
		}
		var cfg Config
		if err := json.Unmarshal(fileData, &cfg); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			http.Error(w, "server_index out of range", http.StatusBadRequest)
			return
		}
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			http.Error(w, "db_index out of range", http.StatusBadRequest)
			return
		}
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}

		db := cfg.Servers[si].Databases[di]
		if db.K8sSecret != "" {
			http.Error(w, "This password lives in a Kubernetes Secret; read it with kubectl", http.StatusBadRequest)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		renderIndex(w, configPath, &cfg, "", &revealedPassword{
			Server:   cfg.Servers[si].Name,
			Database: db.Database,
			User:     db.User,
			Password: db.Password,
		})
	}
}

func handleUpdatePassword(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		si, err := strconv.Atoi(r.FormValue("server_index"))
		if err != nil {
			http.Error(w, "Invalid server_index", http.StatusBadRequest)
			return
		}
		di, err := strconv.Atoi(r.FormValue("db_index"))
		if err != nil {
			http.Error(w, "Invalid db_index", http.StatusBadRequest)
			return
		}
		newPassword := r.FormValue("new_password")
		if newPassword == "" {
			http.Error(w, "new_password is required", http.StatusBadRequest)
			return
		}

		configMu.Lock()
		defer configMu.Unlock()

		fileData, err := os.ReadFile(configPath)
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
			return
		}
		var cfg Config
		if err := json.Unmarshal(fileData, &cfg); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			http.Error(w, "server_index out of range", http.StatusBadRequest)
			return
		}
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			http.Error(w, "db_index out of range", http.StatusBadRequest)
			return
		}
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}
		cfg.Servers[si].Databases[di].Password = newPassword

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Password updated"), http.StatusSeeOther)
	}
}

func handleMigrateDatabase(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		si, err := strconv.Atoi(r.FormValue("server_index"))
		if err != nil {
			http.Error(w, "Invalid server_index", http.StatusBadRequest)
			return
		}
		di, err := strconv.Atoi(r.FormValue("db_index"))
		if err != nil {
			http.Error(w, "Invalid db_index", http.StatusBadRequest)
			return
		}
		targetServer := strings.TrimSpace(r.FormValue("target_server"))
		confirmName := strings.TrimSpace(r.FormValue("confirm_database"))
		confirmDrop := r.FormValue("confirm_drop") == "on" || confirmName != ""

		configMu.Lock()
		defer configMu.Unlock()

		fileData, err := os.ReadFile(configPath)
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
			return
		}
		var cfg Config
		if err := json.Unmarshal(fileData, &cfg); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			http.Error(w, "server_index out of range", http.StatusBadRequest)
			return
		}
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			http.Error(w, "db_index out of range", http.StatusBadRequest)
			return
		}
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}

		if targetServer == "" {
			cfg.Servers[si].Databases[di].Migrate = nil
		} else {
			if _, err := resolveTargetServer(&cfg, targetServer, cfg.Servers[si].Name); err != nil {
				http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: "+err.Error()), http.StatusSeeOther)
				return
			}
			// Dropping the source is irreversible, so it takes the database
			// name typed back, not just a checkbox.
			if dbName := cfg.Servers[si].Databases[di].Database; confirmDrop && confirmName != dbName {
				msg := fmt.Sprintf("Error: migration not scheduled. To drop the source after migrating, type the database name %q exactly. Leave the field empty to keep the source.", dbName)
				http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
				return
			}
			cfg.Servers[si].Databases[di].Migrate = &MigrateConfig{
				TargetServer: targetServer,
				ConfirmDrop:  confirmDrop,
			}
		}

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		msg := "Migration scheduled; the source will be kept"
		if confirmDrop {
			msg = "Migration scheduled; the source will be dropped after it's verified"
		}
		if targetServer == "" {
			msg = "Migration cleared"
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

const staleRowMsg = "Error: the config file changed since this page loaded, so nothing was saved. Check the row and try again."

// staleRow reports whether the row a form was rendered for no longer sits at
// (si, di), e.g. because the config file was edited by hand after the page
// loaded. Acting on the index alone would then change a different database.
// Requests that omit the identity fields (API clients, older pages) skip it.
func staleRow(r *http.Request, cfg *Config, si, di int) bool {
	if name := r.FormValue("server"); name != "" && cfg.Servers[si].Name != name {
		return true
	}
	if db := r.FormValue("database"); db != "" && cfg.Servers[si].Databases[di].Database != db {
		return true
	}
	return false
}

func writeConfigErrMsg(err error) string {
	msg := "Error: couldn't save the config file: " + err.Error()
	if errors.Is(err, syscall.EROFS) || errors.Is(err, os.ErrPermission) {
		msg += ". The file is read-only (a Kubernetes ConfigMap mount is read-only); edit the source config instead."
	}
	return msg
}

func generatePassword() (string, error) {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789!@#$%^&*"
	b := make([]byte, 20)
	for i := range b {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		b[i] = charset[n.Int64()]
	}
	return string(b), nil
}

func handleGeneratePassword(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		si, err := strconv.Atoi(r.FormValue("server_index"))
		if err != nil {
			http.Error(w, "Invalid server_index", http.StatusBadRequest)
			return
		}
		di, err := strconv.Atoi(r.FormValue("db_index"))
		if err != nil {
			http.Error(w, "Invalid db_index", http.StatusBadRequest)
			return
		}

		newPassword, err := generatePassword()
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to generate password"), http.StatusSeeOther)
			return
		}

		configMu.Lock()
		defer configMu.Unlock()

		fileData, err := os.ReadFile(configPath)
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
			return
		}
		var cfg Config
		if err := json.Unmarshal(fileData, &cfg); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			http.Error(w, "server_index out of range", http.StatusBadRequest)
			return
		}
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			http.Error(w, "db_index out of range", http.StatusBadRequest)
			return
		}
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}

		dbName := cfg.Servers[si].Databases[di].Database
		cfg.Servers[si].Databases[di].Password = newPassword

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		// The password is never echoed: ?msg= ends up in browser history and
		// proxy logs. It lives in the config file only.
		msg := fmt.Sprintf("Generated a new password for %s and saved it to the config file. Use Show to view it.", dbName)
		http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

func parseBackupFields(r *http.Request) BackupConfig {
	schedule := r.FormValue("backup_schedule")
	if schedule != "weekly" {
		schedule = "daily"
	}
	keepCount := 0
	if s := strings.TrimSpace(r.FormValue("backup_keep_count")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			keepCount = n
		}
	}
	return BackupConfig{
		Enabled:         r.FormValue("backup_enabled") == "on",
		Schedule:        schedule,
		KeepCount:       keepCount,
		RestoreOnCreate: r.FormValue("backup_restore_on_create") == "on",
	}
}

func handleUpdateBackup(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		si, err := strconv.Atoi(r.FormValue("server_index"))
		if err != nil {
			http.Error(w, "Invalid server_index", http.StatusBadRequest)
			return
		}
		di, err := strconv.Atoi(r.FormValue("db_index"))
		if err != nil {
			http.Error(w, "Invalid db_index", http.StatusBadRequest)
			return
		}

		backup := parseBackupFields(r)

		configMu.Lock()
		defer configMu.Unlock()

		fileData, err := os.ReadFile(configPath)
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
			return
		}
		var cfg Config
		if err := json.Unmarshal(fileData, &cfg); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			http.Error(w, "server_index out of range", http.StatusBadRequest)
			return
		}
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			http.Error(w, "db_index out of range", http.StatusBadRequest)
			return
		}
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}
		cfg.Servers[si].Databases[di].Backup = &backup

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Backup settings updated"), http.StatusSeeOther)
	}
}

func handleRotateSecret(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secretsManager == nil {
			http.Error(w, "Kubernetes secrets mode is not enabled", http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		si, err := strconv.Atoi(r.FormValue("server_index"))
		if err != nil {
			http.Error(w, "Invalid server_index", http.StatusBadRequest)
			return
		}
		di, err := strconv.Atoi(r.FormValue("db_index"))
		if err != nil {
			http.Error(w, "Invalid db_index", http.StatusBadRequest)
			return
		}

		configMu.RLock()
		fileData, err := os.ReadFile(configPath)
		configMu.RUnlock()
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
			return
		}
		var cfg Config
		if err := json.Unmarshal(fileData, &cfg); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			http.Error(w, "server_index out of range", http.StatusBadRequest)
			return
		}
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			http.Error(w, "db_index out of range", http.StatusBadRequest)
			return
		}
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}

		serverName := cfg.Servers[si].Name
		db := cfg.Servers[si].Databases[di]

		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		if _, err := secretsManager.rotateSecret(ctx, cfg.Servers[si].RootConnectionString, db); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to rotate secret: "+err.Error()), http.StatusSeeOther)
			return
		}

		// Reprovision just this one database in the background so the new
		// password actually gets applied to the DB user. This is dispatched
		// as a goroutine (rather than run synchronously) because
		// connectWithRetry can take up to ~50s worst case against an
		// unreachable host, and the HTTP handler should redirect immediately.
		singleServerConfig := &Config{
			Servers: []DatabaseServer{
				{
					Name:                 cfg.Servers[si].Name,
					RootConnectionString: cfg.Servers[si].RootConnectionString,
					DryRun:               cfg.Servers[si].DryRun,
					Databases:            []DatabaseConfig{cfg.Servers[si].Databases[di]},
				},
			},
		}
		go func() {
			if err := processConfig(singleServerConfig); err != nil {
				log.Printf("k8s-secrets: reprovision after rotate for %s/%s failed: %v", serverName, db.Database, err)
			}
		}()

		msg := fmt.Sprintf("Rotated Kubernetes secret %s", db.K8sSecret)
		http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

// handleMoveToSecret moves one database's config password into a Kubernetes
// Secret (named by the form, default <database>-credentials) without changing
// it, then drops it from the config file. A leftover Secret this provisioner
// created earlier is adopted as-is.
func handleMoveToSecret(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if secretsManager == nil {
			http.Error(w, "Kubernetes secrets mode is not enabled", http.StatusBadRequest)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		si, err := strconv.Atoi(r.FormValue("server_index"))
		if err != nil {
			http.Error(w, "Invalid server_index", http.StatusBadRequest)
			return
		}
		di, err := strconv.Atoi(r.FormValue("db_index"))
		if err != nil {
			http.Error(w, "Invalid db_index", http.StatusBadRequest)
			return
		}

		configMu.Lock()
		defer configMu.Unlock()

		fileData, err := os.ReadFile(configPath)
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
			return
		}
		var cfg Config
		if err := json.Unmarshal(fileData, &cfg); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			http.Error(w, "server_index out of range", http.StatusBadRequest)
			return
		}
		if di < 0 || di >= len(cfg.Servers[si].Databases) {
			http.Error(w, "db_index out of range", http.StatusBadRequest)
			return
		}
		if staleRow(r, &cfg, si, di) {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(staleRowMsg), http.StatusSeeOther)
			return
		}

		server := cfg.Servers[si]
		db := &cfg.Servers[si].Databases[di]
		if db.K8sSecret != "" {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(db.Database+" is already in Kubernetes Secret "+db.K8sSecret), http.StatusSeeOther)
			return
		}
		name, err := claimSecretName(&cfg, r.FormValue("secret_name"), db.Database)
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: "+err.Error()), http.StatusSeeOther)
			return
		}

		// Secret first, config second: if the config write fails, the Secret
		// holds the same password the database already has, so nothing breaks.
		db.K8sSecret = name
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		livePassword, err := secretsManager.reconcilePassword(ctx, server.RootConnectionString, *db)
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to create secret: "+err.Error()), http.StatusSeeOther)
			return
		}
		msg := fmt.Sprintf("Moved the password for %s into Kubernetes Secret %s", db.Database, name)
		if db.Password != "" && livePassword != db.Password {
			msg = fmt.Sprintf("Kubernetes Secret %s already existed and is now used for %s; its password replaces the one from the config file", name, db.Database)
		}
		db.Password = ""

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

func handleAddDatabase(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}
		si, err := strconv.Atoi(r.FormValue("server_index"))
		if err != nil {
			http.Error(w, "Invalid server_index", http.StatusBadRequest)
			return
		}

		var permissions []string
		if p := strings.TrimSpace(r.FormValue("permissions")); p != "" {
			for _, perm := range strings.Split(p, ",") {
				permissions = append(permissions, strings.TrimSpace(perm))
			}
		}

		database := strings.TrimSpace(r.FormValue("database"))
		user := strings.TrimSpace(r.FormValue("user"))
		if database == "" || user == "" {
			http.Error(w, "database and user are required", http.StatusBadRequest)
			return
		}

		configMu.Lock()
		defer configMu.Unlock()

		fileData, err := os.ReadFile(configPath)
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
			return
		}
		var cfg Config
		if err := json.Unmarshal(fileData, &cfg); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
			return
		}
		if si < 0 || si >= len(cfg.Servers) {
			http.Error(w, "server_index out of range", http.StatusBadRequest)
			return
		}
		for _, existing := range cfg.Servers[si].Databases {
			if existing.Database == database {
				msg := fmt.Sprintf("Error: %s already has a database named %s", cfg.Servers[si].Name, database)
				http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
				return
			}
		}

		msg := "Database added"
		newDB := DatabaseConfig{
			Database:              database,
			User:                  user,
			Password:              r.FormValue("password"),
			Permissions:           permissions,
			RequiresConnectString: r.FormValue("requires_connect_string") == "on",
		}
		if secretsManager != nil {
			// New databases start out in a Secret; the provisioner generates
			// the password on its first pass.
			name, err := claimSecretName(&cfg, r.FormValue("secret_name"), database)
			if err != nil {
				http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: "+err.Error()), http.StatusSeeOther)
				return
			}
			newDB.K8sSecret = name
			newDB.Password = ""
		} else if newDB.Password == "" {
			if newDB.Password, err = generatePassword(); err != nil {
				http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to generate password"), http.StatusSeeOther)
				return
			}
			msg = "Database added with a generated password. Use Show under Manage to view it."
		}
		if backup := parseBackupFields(r); backup.Enabled || backup.RestoreOnCreate || backup.KeepCount != 0 {
			newDB.Backup = &backup
		}
		cfg.Servers[si].Databases = append(cfg.Servers[si].Databases, newDB)

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape(msg), http.StatusSeeOther)
	}
}

func handleAddServer(configPath string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, "Bad request", http.StatusBadRequest)
			return
		}

		name := strings.TrimSpace(r.FormValue("name"))
		rootConnStr := strings.TrimSpace(r.FormValue("root_connection_string"))
		if name == "" || rootConnStr == "" {
			http.Error(w, "name and root_connection_string are required", http.StatusBadRequest)
			return
		}

		newServer := DatabaseServer{
			Name:                 name,
			RootConnectionString: rootConnStr,
			DryRun:               r.FormValue("dry_run") == "on",
			Databases:            []DatabaseConfig{},
		}

		configMu.Lock()
		defer configMu.Unlock()

		fileData, err := os.ReadFile(configPath)
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to read config"), http.StatusSeeOther)
			return
		}
		var cfg Config
		if err := json.Unmarshal(fileData, &cfg); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to parse config"), http.StatusSeeOther)
			return
		}
		// Names key Kubernetes secrets and migration targets, so they must be unique.
		for _, existing := range cfg.Servers {
			if existing.Name == name {
				http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: a server named "+name+" already exists"), http.StatusSeeOther)
				return
			}
		}
		cfg.Servers = append(cfg.Servers, newServer)

		out, err := json.MarshalIndent(cfg, "", "  ")
		if err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape("Error: failed to serialize config"), http.StatusSeeOther)
			return
		}
		if err := os.WriteFile(configPath, out, 0600); err != nil {
			http.Redirect(w, r, "/?msg="+url.QueryEscape(writeConfigErrMsg(err)), http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/?msg="+url.QueryEscape("Server added"), http.StatusSeeOther)
	}
}
