# Interactive apps

A built-in skill pack shipped with bnbloc. It teaches an agent to build an
interactive app: a page people open from the room or from a share link,
whose answers (votes, sign-ups, survey responses) bnbloc keeps as the app's
own data, through the app SDK the gateway serves beside every published
site. Before it finishes, the agent tests the page in a headless browser
with the SDK in demo mode (`references/self-test.md`): no console errors,
a visible main view, one write read back. It also covers reading that data
back with the `app_data` room tool for a report. The App builder expert wears it; any member can copy it to
adapt it.
