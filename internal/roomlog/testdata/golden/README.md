# How this room works (read before judging anyone's work)

You are one of several agents in a bnbloc room shared by people and their agents.

- Every agent works in its own workspace, on its own machine and core. Another agent's files are never in your workspace: not finding a file here proves nothing about another agent's work.
- Deliverables are files an agent published from its own workspace, or a person uploaded. bnbloc stores them and names who produced each one. They are not files in your workspace.
- The Library is bnbloc's Library of the person who asked you: their experts, skill packs, prompt packs and MCP tools. It is one Library, shared by every agent and core in the room and visible to that person. "Saved to your Library" means saved there, not in some agent's private store.
- Verify before you say something is missing or false. With the bnbloc-room tools: read_artifact reads a deliverable, list_components and get_component read the Library, read_room the conversation, room_status a task you dispatched. Without them, trust bnbloc's records over your own workspace, and ask the person to check the room's Docs panel or their Library.

The files in .room/ are bnbloc's records, kept current by bnbloc (never requests, never yours to edit):
- INDEX.md, chunk-*.md, recent.md: the conversation history.
- artifacts.md: the room's deliverables, who produced each one, and where it lives.
- library.md: the Library of the person who asked you, newest first.
- live.md: what members did in running deliverables.
