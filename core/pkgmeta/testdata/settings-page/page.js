// An example settings page (docs/architecture/settings-pages.md): about the
// smallest page a component could ship, written against the settings page
// protocol by hand (a real page would use @bnbloc/previewer-sdk/settings).
// It reads the settings of the scope it edits, renders an input per field (a
// secret only as set or not), saves through the platform, and shows a
// refusal at its field. The gateway tests serve it; the web host's tests
// drive it (web/src/lib/settingsPageFrame.test.ts).
(function () {
  "use strict";
  const host = new URLSearchParams(window.location.hash.slice(1)).get("host") || "";
  const parent = window.parent;
  if (!host || parent === window) return;

  let nextId = 1;
  const pending = new Map();
  let inputs = [];
  const status = document.getElementById("status");

  const post = (msg) => parent.postMessage(msg, "*");
  const ask = (msg) =>
    new Promise((resolve) => {
      const id = nextId++;
      pending.set(id, resolve);
      post({ ...msg, id });
    });

  function render(view) {
    const form = document.getElementById("fields");
    form.replaceChildren();
    inputs = view.fields.map((f) => {
      const label = document.createElement("label");
      label.textContent = f.label + (f.required ? " *" : "");
      const input = document.createElement("input");
      input.name = f.key;
      input.type = f.type === "secret" ? "password" : "text";
      input.value = f.type === "secret" ? "" : f.value || "";
      input.placeholder = f.type === "secret" ? (f.set ? "set" : "not set") : f.default || "";
      label.appendChild(input);
      form.appendChild(label);
      return { field: f, input };
    });
    status.textContent = view.missing.length > 0 ? "Needs setup: " + view.missing.join(", ") : "Set up.";
  }

  async function load() {
    const answer = await ask({ type: "settings.get" });
    if (answer.type === "settings.view") render(answer.view);
    else status.textContent = answer.reason;
  }

  async function save() {
    const values = {};
    for (const { field, input } of inputs) {
      input.setAttribute("aria-invalid", "false");
      // An empty secret keeps the stored one: it is not sent.
      if (field.type !== "secret" || input.value !== "") values[field.key] = input.value;
    }
    const answer = await ask({ type: "settings.set", values });
    if (answer.type === "settings.view") {
      render(answer.view);
      status.textContent = "Saved. " + status.textContent;
      return;
    }
    status.textContent = answer.reason;
    const refused = inputs.find((i) => i.field.key === answer.key);
    if (refused) refused.input.setAttribute("aria-invalid", "true");
  }

  window.addEventListener("message", (ev) => {
    if (ev.source !== parent || ev.origin !== host) return;
    const msg = ev.data || {};
    if (msg.type === "settings.init") {
      document.documentElement.setAttribute("data-theme", msg.theme);
      void load();
      return;
    }
    const resolve = pending.get(msg.id);
    if (resolve) {
      pending.delete(msg.id);
      resolve(msg);
    }
  });
  document.getElementById("save").addEventListener("click", () => void save());
  document.getElementById("close").addEventListener("click", () => post({ type: "settings.close" }));
  post({ type: "settings.ready" });
})();
