import { lazy, Suspense, useCallback, useEffect } from "react";
import { useAuth } from "@/lib/auth";
import { startPreviewerSync } from "@/views/artifact/remotePreviewers";
import { ensureBridgeRunning, desktop } from "@/lib/desktop";
import { gatewayBase } from "@/lib/config";
import { routeHash, useHashRoute } from "@/lib/useHashRoute";
import { artifactLinks, listenForSharedComposerRefs, messageJumps } from "@/lib/team";
import { listenForPushNavigation } from "@/lib/webPush";
import { Shell, type View } from "@/views/Shell";
import { Plaza } from "@/views/Plaza";
import { MyRooms } from "@/views/MyRooms";
import { isPreviewRoute, peopleChatRoomFromHash } from "@/lib/previewRoute";
import { useDesktopUpdate } from "@/views/DesktopUpdateWatcher";
import { DesktopPresenceProvider } from "@/lib/desktopPresence";
import { DesktopConnectNotice } from "@/views/DesktopConnect";
import { MacInstallGuideHost } from "@/views/MacInstallGuideHost";
import { useWebUpdate, WebUpdateNotice } from "@/views/WebUpdateNotice";
import { GettingStarted } from "@/views/onboarding/GettingStarted";
import { TourOverlay } from "@/views/onboarding/TourOverlay";

// Surfaces load on demand so the first paint ships only the shell and the
// landing views: sign-in is not downloaded by a signed-in member, a room's
// conversation code arrives when a room opens, and the platform console,
// release editor and component workbench only for those who open them.
const Login = lazy(() => import("@/views/Login").then((m) => ({ default: m.Login })));
const RoomView = lazy(() => import("@/views/RoomView").then((m) => ({ default: m.RoomView })));
const Agents = lazy(() => import("@/views/Agents").then((m) => ({ default: m.Agents })));
const Library = lazy(() => import("@/views/library/Library").then((m) => ({ default: m.Library })));
const GlobalSearch = lazy(() => import("@/views/search/GlobalSearch").then((m) => ({ default: m.GlobalSearch })));
const Team = lazy(() => import("@/views/Team").then((m) => ({ default: m.Team })));
const MemberComponents = lazy(() => import("@/views/MemberComponents").then((m) => ({ default: m.MemberComponents })));
const Organization = lazy(() => import("@/views/Organization").then((m) => ({ default: m.Organization })));
const Directory = lazy(() => import("@/views/Directory").then((m) => ({ default: m.Directory })));
const ReleaseHub = lazy(() => import("@/views/ReleaseHub").then((m) => ({ default: m.ReleaseHub })));
const Settings = lazy(() => import("@/views/Settings").then((m) => ({ default: m.Settings })));
const StandalonePreview = lazy(() =>
  import("@/views/StandalonePreview").then((m) => ({ default: m.StandalonePreview })),
);
const StandalonePeopleChat = lazy(() =>
  import("@/views/StandalonePeopleChat").then((m) => ({ default: m.StandalonePeopleChat })),
);

function Spinner() {
  return (
    <div className="grid min-h-[50dvh] place-items-center">
      <div
        role="status"
        aria-label="Loading"
        className="size-6 animate-spin rounded-full border-2 border-[var(--color-border-strong)] border-t-[var(--color-accent)]"
      />
    </div>
  );
}

export function App() {
  const { subject, tenant, ready, tokenEra } = useAuth();
  const [route, navigate] = useHashRoute();
  // Derive the shell's view/room from the URL hash so rooms and top-level views
  // are deep-linkable and survive reload/back-forward. Preview windows are
  // handled separately below and never reach this.
  const room = route.kind === "room" ? route.roomId : null;
  const view = (route.kind === "view" ? route.view : "plaza") as View;

  // A deep link to one message (a notification's #/room/<id>?seq=<n>): hand
  // the anchor to the room feed's jump mechanism (the same one global search
  // uses); a deep link to a deliverable (#/room/<id>?artifact=<aid>[&v=n])
  // goes to the room's preview workbench. Then drop the anchor from the URL
  // so a reload does not jump or reopen again.
  useEffect(() => {
    if (route.kind !== "room" || (!route.seq && !route.artifactId)) return;
    if (route.seq) messageJumps.queue(route.roomId, { seq: route.seq, channelId: route.channelId });
    if (route.artifactId) artifactLinks.queue(route.roomId, { artifactId: route.artifactId, version: route.artifactVersion });
    window.history.replaceState(null, "", routeHash({ kind: "room", roomId: route.roomId }));
  }, [route]);
  // A clicked browser notification opens its deep link in this tab.
  useEffect(() => listenForPushNavigation(), []);
  // "Ask the agent about this" from a popped-out people chat window lands in
  // this window's room composer.
  useEffect(() => listenForSharedComposerRefs(), []);
  // Background desktop update check; surfaces as a sidebar entry, never a popup
  // (forced updates excepted). Called before the early returns below.
  const desktopUpdate = useDesktopUpdate(!!subject);
  const webUpdate = useWebUpdate(!!subject);

  // Zero-config local bridge: once signed in, the desktop shell seeds and
  // starts the bridge for this member so room chat has a runner. No-op in a
  // plain browser and after the first configured launch.
  useEffect(() => {
    if (subject) void ensureBridgeRunning(subject, gatewayBase(), tenant);
  }, [subject, tenant]);

  // The shell's managed sidecar adapters authenticate to the gateway with the
  // member's session token, and the supervised bridge renews its own session
  // from the member's refresh token: both are fed once signed in and re-fed on
  // every swap (tokenEra bumps on sign-in, renewal and tenant switch), so the
  // shell always holds the newest tokens. Signing out re-feeds an empty
  // session, which stops the shell handing a refresh token to the bridge.
  useEffect(() => {
    if (ready) void desktop.refreshInternalAdapter(gatewayBase());
  }, [ready, subject, tokenEra]);

  // Installed previewer components join the viewer registry from the
  // member's catalog (rollout + tenant gated); re-read on sign-in, tenant
  // switch and periodically.
  useEffect(() => (subject ? startPreviewerSync() : undefined), [subject, tenant]);

  const openRoom = useCallback((id: string) => navigate({ kind: "room", roomId: id }), [navigate]);
  const closeRoom = useCallback(() => navigate({ kind: "view", view: "myrooms" }), [navigate]);
  const openView = useCallback((v: View) => navigate({ kind: "view", view: v }), [navigate]);

  if (!ready) {
    return (
      <div className="grid min-h-[100dvh] place-items-center bg-[var(--color-bg)]">
        <div className="size-6 animate-spin rounded-full border-2 border-[var(--color-border-strong)] border-t-[var(--color-accent)]" />
      </div>
    );
  }

  if (!subject) {
    return (
      <Suspense fallback={<Spinner />}>
        <Login />
      </Suspense>
    );
  }

  // A popped-out people chat window renders only that room's people chat.
  const peopleChatRoom = peopleChatRoomFromHash(window.location.hash);
  if (peopleChatRoom) {
    return (
      <Suspense fallback={<Spinner />}>
        <StandalonePeopleChat roomId={peopleChatRoom} />
      </Suspense>
    );
  }

  // A torn-out preview window renders only its artifact (no shell, no room):
  // the hash route is the whole window's identity. Auth still gates it — a
  // preview window whose session handoff failed lands on Login like any
  // other window, and signs straight into its artifact from there. Checked
  // after the hooks and gates above so hook order never varies.
  const preview = isPreviewRoute();
  if (preview) {
    return (
      <Suspense fallback={<Spinner />}>
        <StandalonePreview roomId={preview.roomId} artifactId={preview.artifactId} />
      </Suspense>
    );
  }

  return (
    <DesktopPresenceProvider subject={subject}>
      {desktopUpdate.dialog}
      <MacInstallGuideHost />
      <Suspense fallback={null}>
        <GlobalSearch onOpenRoom={openRoom} />
      </Suspense>
      <TourOverlay subject={subject} inRoom={room !== null} />
      <Shell
        footerNotice={(collapsed, closeDrawer) => (
          <>
            {webUpdate && <WebUpdateNotice collapsed={collapsed} />}
            {desktopUpdate.renderNotice(collapsed)}
            <DesktopConnectNotice collapsed={collapsed} />
            <GettingStarted
              subject={subject}
              collapsed={collapsed}
              activeRoom={room}
              onOpenRoom={openRoom}
              onView={openView}
              onNavigate={closeDrawer}
            />
          </>
        )}
        view={view}
        activeRoom={room}
        onOpenRoom={openRoom}
        onView={openView}
      >
        <Suspense fallback={<Spinner />}>
          {room ? (
            // Keyed by room: every per-room and per-channel piece of state
            // starts fresh on a room switch (nothing can leak across rooms).
            <RoomView key={room} roomId={room} onBack={closeRoom} onOpenRoom={openRoom} />
          ) : view === "agents" ? (
            <Agents />
          ) : view === "library" ? (
            <Library onOpenRoom={openRoom} />
          ) : view === "team" ? (
            <Team onOpenRoom={openRoom} />
          ) : view === "components" ? (
            <MemberComponents />
          ) : view === "org" ? (
            <Organization />
          ) : view === "directory" ? (
            <Directory />
          ) : view === "releases" ? (
            <ReleaseHub />
          ) : view === "settings" ? (
            <Settings />
          ) : view === "plaza" ? (
            <Plaza onOpen={openRoom} />
          ) : (
            <MyRooms onOpen={openRoom} />
          )}
        </Suspense>
      </Shell>
    </DesktopPresenceProvider>
  );
}
