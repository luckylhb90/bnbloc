import { FIRST_COMPOSER_KEY, nextComposerKey } from "./composerKey.ts";

function assert(condition: boolean, message: string): void {
  if (!condition) throw new Error(message);
}

// The default channel resolving keeps the composer (and what is typed in it).
let s = nextComposerKey(FIRST_COMPOSER_KEY, null);
assert(s === FIRST_COMPOSER_KEY, "no change, same state");
s = nextComposerKey(s, "ch_general");
assert(s.key === "", "the first channel to resolve keeps the composer's key");
assert(nextComposerKey(s, "ch_general") === s, "the same channel changes nothing");

// Another channel is another conversation: a fresh composer, once per channel.
s = nextComposerKey(s, "ch_ops");
assert(s.key === "ch_ops", "another channel replaces the composer");
s = nextComposerKey(s, "ch_general");
assert(s.key === "ch_general", "and back");

// A channel that went stale and is re-selected from nothing is a change too.
s = nextComposerKey(s, null);
assert(s.key === "", "no channel");
s = nextComposerKey(s, "ch_ops");
assert(s.key === "ch_ops", "a channel after one was lost is a fresh composer");
