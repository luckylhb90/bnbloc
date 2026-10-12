import { ATTACHMENT_TICKET_SLACK_MS, createAttachmentTicketCache } from "./attachmentTickets.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

async function testTicketsAreReusedUntilNearExpiryAndKeyedBySession(): Promise<void> {
  let clock = 1_000_000;
  let minted = 0;
  const cache = createAttachmentTicketCache(async (roomId, id) => {
    minted++;
    return { url: `/v1/rooms/${roomId}/attachments/${id}?ticket=t${minted}`, expiresAtMs: clock + 15 * 60_000 };
  }, () => clock);

  assert(cache.cached("s1", "r", "a") === null, "nothing cached before the first mint");
  const [u1, u2] = await Promise.all([cache.url("s1", "r", "a"), cache.url("s1", "r", "a")]);
  assert(u1 === u2 && minted === 1, "concurrent requests share one mint");
  assert(cache.cached("s1", "r", "a") === u1, "a fresh ticket is served from the cache");
  assert((await cache.url("s1", "r", "b")) !== u1, "each attachment has its own ticket");
  assert((await cache.url("s2", "r", "a")) !== u1, "another session never reuses a ticket");

  clock += 15 * 60_000 - ATTACHMENT_TICKET_SLACK_MS;
  assert(cache.cached("s1", "r", "a") === null, "a ticket about to expire is not handed out");
  const renewed = await cache.url("s1", "r", "a");
  assert(renewed !== u1 && cache.cached("s1", "r", "a") === renewed, "an expiring ticket is renewed");
}

async function testFailedMintIsRetried(): Promise<void> {
  let fail = true;
  const cache = createAttachmentTicketCache(async () => {
    if (fail) throw new Error("offline");
    return { url: "/u?ticket=x", expiresAtMs: Date.now() + 600_000 };
  });
  let threw = false;
  await cache.url("s", "r", "a").catch(() => (threw = true));
  assert(threw, "a failed mint rejects");
  fail = false;
  assert((await cache.url("s", "r", "a")) === "/u?ticket=x", "the next request mints again");
}

await testTicketsAreReusedUntilNearExpiryAndKeyedBySession();
await testFailedMintIsRetried();
