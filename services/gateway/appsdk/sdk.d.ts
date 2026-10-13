// Types of the bnbloc app SDK (~app/sdk.js; served beside it as
// ~app/sdk.d.ts). Load the SDK with <script src="~app/sdk.js"></script>; it
// defines `bnblocApp`, and `app` unless the page defines its own.
//
// Data model: each app owns named collections of JSON documents. The
// server stamps every document (_id, _by, _createdAt, _updatedAt,
// _version; _author for the app's owners only) and a client never sets
// them. A document is private to its author and the app's owners unless it
// was written with {public: true}; only its author or an owner may change
// or delete it. _by and me().id are the same per-app pseudonym, so
// `put((await app.me()).id, doc, {ifAbsent: true})` is one record per
// person. Caps: 64 KiB per document, 100k documents or 256 MiB per app, 64
// collections per app.
//
// In the app gallery's "Try demo" the same calls are answered by an empty,
// in-memory data space of the page alone (me() is {id: "demo"}); nothing
// is saved or shared, and a reload starts empty again.

export {};

declare global {
  /** The stamps the server puts on every document. */
  interface BnblocAppStamps {
    _id: string;
    /** The author's per-app pseudonym (equals their me().id). */
    _by: string;
    _createdAt: number;
    _updatedAt: number;
    _version: number;
    /** The real author, for the app's owners only. */
    _author?: string;
  }

  type BnblocAppDoc<T> = T & BnblocAppStamps;

  /** One page of a find; pass `next` as `after` for the following page. */
  interface BnblocAppPage<T> {
    docs: BnblocAppDoc<T>[];
    next?: string;
  }

  /** A filter on top-level or dotted fields: a value (equality) or an
   *  operator object. Other operators are refused. */
  type BnblocAppFilter = Record<
    string,
    | unknown
    | {
        $eq?: unknown;
        $ne?: unknown;
        $in?: unknown[];
        $gt?: unknown;
        $gte?: unknown;
        $lt?: unknown;
        $lte?: unknown;
        $exists?: boolean;
      }
  >;

  /** One change of the app's data. version 0: the document was deleted;
   *  no collection: all of the app's data was deleted. Metadata only:
   *  re-read what changed. */
  interface BnblocAppChange {
    appId: string;
    collection?: string;
    id?: string;
    version: number;
  }

  /** A refused or failed call: status is the HTTP status (404 not found
   *  or not readable, 409 a version or ifAbsent conflict, 413 too large,
   *  429 slow down). */
  interface BnblocAppError extends Error {
    status: number;
    code: string;
  }

  interface BnblocAppCollection<T = Record<string, unknown>> {
    /** Inserts a new document with a server id (private unless public). */
    insert(doc: T, opts?: { public?: boolean }): Promise<BnblocAppDoc<T>>;
    /** Finds documents in _id order (creation order for server ids). */
    find(
      filter?: BnblocAppFilter,
      opts?: { sort?: "_id" | "-_id"; limit?: number; after?: string },
    ): Promise<BnblocAppPage<T>>;
    get(id: string): Promise<BnblocAppDoc<T>>;
    /** Replaces (or creates) the document with this id. ifAbsent: create
     *  only (409 when it exists); ifVersion: only over that version (409
     *  when it moved on). public: unchanged when absent. */
    put(id: string, doc: T, opts?: { ifAbsent?: boolean; ifVersion?: number; public?: boolean }): Promise<BnblocAppDoc<T>>;
    /** Merges fields (a JSON merge patch) and adds numbers ($inc) on the
     *  locked document: concurrent $inc lose nothing. */
    patch(
      id: string,
      ops: { merge?: Partial<T> | Record<string, unknown>; $inc?: Record<string, number>; public?: boolean },
      opts?: { ifVersion?: number },
    ): Promise<BnblocAppDoc<T>>;
    remove(id: string, opts?: { ifVersion?: number }): Promise<void>;
  }

  interface BnblocApp {
    collection<T = Record<string, unknown>>(name: string): BnblocAppCollection<T>;
    /** The caller's per-app id, and a signed-in member's display name. */
    me(): Promise<{ id: string; name?: string }>;
    /** Calls fn with each change until the returned function is called.
     *  Live only: changes made while disconnected are not replayed. */
    subscribe(fn: (change: BnblocAppChange) => void): () => void;
  }

  const bnblocApp: BnblocApp;
  const app: BnblocApp;
}
