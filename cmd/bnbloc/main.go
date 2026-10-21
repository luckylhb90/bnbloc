// Command bnbloc is the operator CLI for the bnbloc control plane. It signs in
// through the gateway's SSO, publishes component packages and desktop app
// updates, and manages agent-core extensions over the gateway's existing
// super-admin HTTP API. See docs/cli.md.
//
//	bnbloc login --gateway https://gw.example.com      # browser SSO, stores a refresh token
//	bnbloc cores publish-sidecar --name acme --local-url http://127.0.0.1:7099 \
//	    --binary darwin-arm64=dist/acme --rollout optin
//	bnbloc updates publish --archive bnbloc.app.tar.gz --signature bnbloc.app.tar.gz.sig --version 1.2.3
//
// Endpoint: --gateway > BNBLOC_GATEWAY > the gateway of the stored sign-in.
// Credential: --token > BNBLOC_TOKEN > BNBLOC_API_TOKEN > the stored sign-in (~/.bnbloc/config.json).
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "login":
		err = runLogin(os.Args[2:])
	case "logout":
		err = runLogout(os.Args[2:])
	case "whoami":
		err = runWhoami(os.Args[2:])
	case "cores":
		err = runCores(os.Args[2:])
	case "components":
		err = runComponents(os.Args[2:])
	case "content":
		err = runContent(os.Args[2:])
	case "previewers":
		err = runPreviewers(os.Args[2:])
	case "updates":
		err = runUpdates(os.Args[2:])
	case "keys":
		err = runKeys(os.Args[2:])
	case "smoke":
		err = runSmoke(os.Args[2:])
	case "alert":
		err = runAlert(os.Args[2:])
	case "api-tokens":
		err = runAPITokens(os.Args[2:])
	case "search":
		err = runSearch(os.Args[2:])
	case "schedules":
		err = runSchedules(os.Args[2:])
	case "events":
		err = runEvents(os.Args[2:])
	case "org-sync":
		err = runOrgSync(os.Args[2:])
	case "version":
		err = runVersion(os.Args[2:])
	case "-h", "--help", "help":
		usage()
		return
	default:
		fmt.Fprintf(os.Stderr, "bnbloc: unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "bnbloc: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `bnbloc — control-plane operator CLI (docs/cli.md)

Sign-in:
  bnbloc login [--gateway <url>]          browser SSO; stores a refresh token in ~/.bnbloc/config.json
  bnbloc whoami [--gateway <url>]         subject / tenant / role of the stored sign-in
  bnbloc logout                           forget the stored sign-in

Components:
  bnbloc cores publish-sidecar --name <n> --binary <goos-goarch>=<path>... [--local-url <url>]
        [--version v] [--publisher p] [--listen-env VAR] [--supervised-env VAR] [--manifest f]
        [--rollout auto|optin|off] [--seckey f | --unsigned]
  bnbloc cores publish-mcp --package <mcp.json> [--binary <goos-goarch>=<path>...] [--settings-page <dir|tar>]
        [--rollout auto|optin|off] [--seckey f | --unsigned]
        an MCP tool: a stdio server's binaries are uploaded and pinned; an endpoint takes none
        (both: a settingsPage's bundle, --settings-page, is uploaded and pinned too)
  bnbloc cores install --url <daemonURL> --name <name> [--publisher p] [--rollout r] [--seckey f | --unsigned]
  bnbloc cores install --manifest <file> --name <name> [--rollout r] [--seckey f | --unsigned]
  bnbloc cores install --package <file> [--signature <file>] [--rollout r]   any pre-built package
        (a notifier package prints its signing secret once)
  bnbloc cores list
  bnbloc cores uninstall <name>
  bnbloc cores enable <name> (--tenant <id>... | --all-tenants)
  bnbloc components publish-package --package <file> --binary <goos-goarch>=<path>... [--settings-page <dir|tar>]
        [--rollout auto|optin|off] [--seckey f | --unsigned]
        a package file without artifacts (a plugin, a notifier sidecar): its binaries are uploaded and pinned,
        and a settingsPage's bundle (--settings-page) too
  bnbloc components rotate-secret <name> [--kind auth-provider]  new endpoint component credential (printed once)
  bnbloc components test <name>           send a test notification through a notifier to you
  bnbloc content check [--json] (<dir>... | --all <root>)
        validate content package directories as a save would (an assist under a task's name meets its contract)
  bnbloc content contracts                print the assist output contracts (what a task's assist must answer)
  bnbloc content publish (--dir <dir> | --all <root>) [--publisher p] [--rollout auto|off] [--seckey f | --unsigned]
        pack, upload, sign and install content package directories for every organization
        (the default content: bnbloc content publish --all packages from its repository)
  bnbloc content export <id> [--version v] [--publisher p] [--out f]
        the signable package of an expert, skill pack or prompt pack version (sign it offline,
        then bnbloc cores install --package f --signature <sig>)
  bnbloc content promote <id> [--version v] [--publisher p] [--rollout auto|off] [--seckey f | --unsigned]
        export, sign and install it for every organization
  bnbloc previewers pack <dir> --out <bundle.tar>   archive a built previewer (prints its sha256)
  bnbloc previewers publish --package <pkg.json> (--bundle <tar> | --dir <dir>) [--rollout r] [--seckey f | --unsigned]
        upload the bundle, pin it in the package, sign, install

Desktop updates:
  bnbloc updates publish --archive <bnbloc.app.tar.gz> --signature <file.sig> --version <semver>
        [--notes <text> | --notes-file <file>] [--target darwin-aarch64|darwin-x86_64]
  bnbloc updates status

Room schedules (as the signed-in member, or BNBLOC_API_TOKEN's subject):
  bnbloc schedules list <room>            the room's scheduled tasks: id, creator, recipient, when, state
  bnbloc schedules run [--dry] <room> <schedule>
                                          run one now, as its creator (only the creator may);
                                          an AI task's run that fails exits non-zero
  bnbloc events send <room> <type> [--subject <s>] [--data <json>|@<file>]
                                          tell the room something happened: its tasks taking it run

Post-deploy smoke:
  bnbloc smoke [--agent <provider|core/provider|targetId|owner/provider>] [--timeout 120s] [--keep]
        throwaway room: stream-first chat echo, history, optional agent reply, delete;
        one PASS/FAIL line per step, non-zero exit on any FAIL
        [--alert-on-failure [--alert-to id,...]]  alert platform admins when the run fails
        [--scenario long-task [--between <cmd>]]  attachment, workspace file, memory across a step
        [--scenario two-members --teammate-agent <provider>]  a second member (the API token in
                                          BNBLOC_TEAMMATE_API_TOKEN) reads A's room and borrows A's agent

Deployment (super-admin):
  bnbloc version                          every service instance's commit + the web bundle's;
                                          non-zero exit when they are not all on one commit

Alerts and API tokens (super-admin):
  bnbloc alert --title <t> [--text <t>] [--url <u>] [--to id,...]   alert via the installed notifiers
                                          (default recipients: every platform super-admin)
  bnbloc api-tokens create --label <l> [--subject svc:<name>] [--ttl-days 90] [--tenant <id>]
                                          long-lived token for unattended runs, printed once
  bnbloc api-tokens list
  bnbloc api-tokens revoke <id>

Search (super-admin):
  bnbloc search reindex [--cursor <c>] [--batches n]   bring the message search index up to date
                                          with the room feed (MySQL: rows stored without search text;
                                          OpenSearch: every message's document); resumable, idempotent,
                                          safe while serving
  bnbloc search status                    search backend, readiness, index health and update queue

Organization sync (super-admin):
  bnbloc org-sync rules | set-rules --file <f>   the group/department/email-domain mapping rules
  bnbloc org-sync preview --email <e> [--group g]... [--department d]   dry run for one member
  bnbloc org-sync deprovision <userId>... | --file <ids|->   end departed users' access
  bnbloc org-sync resync                  re-apply the rules to every member a sign-in recorded

Publisher keys:
  bnbloc keys gen --out <prefix> [--force]   writes <prefix>.pub and <prefix>.key (0600)
  bnbloc keys gen-vapid                      prints a browser push (VAPID) key pair as env lines
  bnbloc keys add --pubkey <file> [--label l]
  bnbloc keys list
  bnbloc keys rm <keyId>

Signing commands default --seckey to ~/.bnbloc/component-publisher.key when it exists.

Global flags (every gateway command):
  --gateway <url>   else BNBLOC_GATEWAY, else the stored sign-in's gateway (default http://localhost:7090)
  --token <token>   else BNBLOC_TOKEN, else BNBLOC_API_TOKEN (exchanged for a session),
                    else the stored sign-in (refreshed per command)
`)
}
