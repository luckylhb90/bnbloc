// What the account switcher says of each account beside its name
// (docs/prd/2026-10-22-multi-account.md §2.5): the shell's state of the
// account (its session, whether its agents serve from this computer, its
// bridge, its running turns). Pure, so the words are unit tested.

import { accountKey } from "./vault.ts";
import type { AccountStatus } from "./desktop.ts";

/** The origin of a gateway URL ("" stays ""): how the shell and the vault name one gateway. */
export function gatewayOrigin(gateway: string): string {
  try {
    return new URL(gateway).origin;
  } catch {
    return gateway.trim().replace(/\/+$/, "");
  }
}

/** The vault key of the account a shell status is about. */
export function statusKey(status: Pick<AccountStatus, "gatewayUrl" | "subject">): string {
  return accountKey(gatewayOrigin(status.gatewayUrl), status.subject);
}

/** The vault key as the shell names accounts (the gateway as an origin). */
export function shellKey(gateway: string, subject: string): string {
  return accountKey(gatewayOrigin(gateway), subject);
}

/** A short phrase for an account's state, or "" when there is nothing to say. */
export function describeStatus(status: AccountStatus | undefined): string {
  if (!status) return "";
  if (status.session === "signedOut") return "sign in again";
  if (status.session === "retrying") return "connecting…";
  if (!status.canServe) return "";
  if (!status.serving) return "agents off on this computer";
  switch (status.bridge) {
    case "running":
      return status.runningTurns > 0 ? `agents online · ${status.runningTurns} running` : "agents online";
    case "starting":
      return "agents starting…";
    case "failed":
      return "agents not starting";
    case "stopped":
      return "agents offline";
  }
}
