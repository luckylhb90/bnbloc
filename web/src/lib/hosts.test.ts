import { hostList } from "./hosts.ts";

function eq(a: unknown, b: unknown, message: string): void {
  if (JSON.stringify(a) !== JSON.stringify(b)) throw new Error(`${message}: got ${JSON.stringify(a)}, want ${JSON.stringify(b)}`);
}

eq(hostList(" Docs.Example.com, api.example.com\ndocs.example.com  "), ["docs.example.com", "api.example.com"], "split, lower-cased, de-duplicated");
eq(hostList(""), [], "nothing");
