export interface HandoffAgent {
  readonly online: boolean;
}

export function isPhoneBrowser(userAgent: string): boolean {
  return /(?:iPhone|iPod|Windows Phone|IEMobile|BlackBerry|BB10|Opera Mini|Android.*Mobile)/i.test(
    userAgent,
  );
}

export function shouldShowDesktopHandoff(input: {
  readonly subject: string | null;
  readonly inDesktopShell: boolean;
  readonly userAgent: string;
  readonly ownAgents: readonly HandoffAgent[] | undefined;
  readonly roomAgents: readonly { ownerUserId: string }[] | undefined;
  readonly roomRosterKnown: boolean;
}): boolean {
  const subject = input.subject?.trim().toLowerCase();
  const ownRoomAgentOnline =
    !!subject && (input.roomAgents ?? []).some((agent) => agent.ownerUserId.toLowerCase() === subject);
  return (
    !input.inDesktopShell &&
    !isPhoneBrowser(input.userAgent) &&
    input.roomRosterKnown &&
    input.roomAgents !== undefined &&
    input.ownAgents !== undefined &&
    !input.ownAgents.some((agent) => agent.online) &&
    !ownRoomAgentOnline
  );
}

export function isGatewayArtifactUrl(
  artifactUrl: string,
  gatewayUrl: string,
  pageOrigin: string,
): boolean {
  try {
    const artifact = new URL(artifactUrl, pageOrigin);
    const gateway = new URL(gatewayUrl, pageOrigin);
    return (
      artifact.origin === gateway.origin &&
      artifact.pathname.startsWith("/v1/artifacts/")
    );
  } catch {
    return false;
  }
}
