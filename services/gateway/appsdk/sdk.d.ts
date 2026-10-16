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
// collections per app. Files (app.files): 25 MiB each, 10000 or 2 GiB per
// app, the same types a chat attachment may be.
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

  /** A file a page uploaded. Keep its id in a document. */
  interface BnblocAppFile {
    id: string;
    name: string;
    /** The attachment type it was checked as (image/png, application/pdf, …). */
    type: string;
    size: number;
    public: boolean;
    /** The uploader's per-app pseudonym (equals their me().id). */
    _by: string;
    _createdAt: number;
    /** The real uploader, for the app's owners only. */
    _author?: string;
  }

  interface BnblocAppFiles {
    /** Uploads a file (images, PDF, Office and OpenDocument files, text,
     *  audio, video, zip; at most 25 MiB): private to its uploader and the
     *  app's owners unless public. 415 for a type not accepted. */
    upload(file: File | Blob, opts?: { public?: boolean }): Promise<BnblocAppFile>;
    get(id: string): Promise<BnblocAppFile>;
    /** A blob: URL of the file's bytes, for <img src> or a download link.
     *  Revoke it (URL.revokeObjectURL) when no longer shown. */
    url(id: string): Promise<string>;
    /** Deletes the file (its uploader or an owner). */
    remove(id: string): Promise<void>;
  }

  interface BnblocApp {
    collection<T = Record<string, unknown>>(name: string): BnblocAppCollection<T>;
    /** The caller's per-app id, and for a signed-in caller (a member, or
     *  a person who signed in to open the link) their display name and
     *  account: their login (what they sign in with) and the email their
     *  identity provider asserted. An anonymous visitor (a link anyone may
     *  open) has no account. Only ever the caller's own. */
    me(): Promise<{ id: string; name?: string; account?: { login: string; email?: string } }>;
    /** Files uploaded by the app's pages. */
    files: BnblocAppFiles;
    /** Asks the organization's AI model. prompt is the app's instruction,
     *  input the data to work on (an object is sent as its JSON), given to
     *  the model as data. json: answers {json} (an object) instead of
     *  {text}. Signed-in callers only: 403 code "sign_in_required" for
     *  an anonymous visitor, 409 "ai_unavailable" where the organization
     *  has no model or turned AI off, 429 when the caller asks too often. */
    ai(req: { prompt: string; input?: string | object; json?: boolean }): Promise<{ text?: string; json?: Record<string, unknown> }>;
    /** Draws images on the organization's AI (count of them in one call,
     *  at most 4): they come back inline (images: [{dataUrl, mimeType,
     *  width, height, revisedPrompt?}]), never a link out. With image (a
     *  previous reply's dataUrl) the provider changes that image instead of
     *  drawing a new one. aspect: "square" (default) | "landscape" |
     *  "portrait". Signed-in callers only, like ai itself. */
    ai.image(req: { prompt: string; aspect?: "square" | "landscape" | "portrait"; count?: number; image?: string }): Promise<{ images: { dataUrl: string; mimeType: string; width: number; height: number; revisedPrompt?: string }[] }>;
    /** Turns spoken audio (a Blob or File from getUserMedia or an input) into
     *  text. Signed-in callers only, like ai itself. */
    ai.voice(blob: Blob, filename?: string): Promise<{ text: string }>;
    /** Sends a notice through the organization's notifications (its chat
     *  bot, desktop, browser push: whatever each person uses). to: "room"
     *  is the room's members (any signed-in caller); a list of logins (the
     *  organization's people) or "group:<name>" (a group chat the
     *  organization's notifier knows; 409 "groups_unavailable" when none
     *  does) are for the app's owners only. Each
     *  person's notification settings apply, the sender is never notified,
     *  and it links to this app unless url (on this deployment) is given.
     *  Plain text. Signed-in callers only (403 "sign_in_required"); 429
     *  when the app sends too many. In the demo nothing is sent. */
    notify(n: { to: "room" | `group:${string}` | string[]; title: string; text?: string; url?: string }): Promise<{ recipients: number }>;
    /** The group chats the room's members connected, to send to with
     *  notify({to: `group:${name}`}). The app's owners see them; anyone else
     *  gets an empty list. Signed-in callers only. In the demo: none. */
    groups(): Promise<{ name: string }[]>;
    /** Calls fn with each change until the returned function is called.
     *  Live only: changes made while disconnected are not replayed. */
    subscribe(fn: (change: BnblocAppChange) => void): () => void;
  }

  const bnblocApp: BnblocApp;
  const app: BnblocApp;
}
