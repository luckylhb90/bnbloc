import { continuedMarker } from "./continued.ts";

function eq<T>(got: T, want: T, what: string): void {
  if (JSON.stringify(got) !== JSON.stringify(want)) throw new Error(`${what}: got ${JSON.stringify(got)}, want ${JSON.stringify(want)}`);
}

eq(continuedMarker({ continuedFrom: { jobId: "j", component: "conn" } }), "conn", "a continue names its service");
eq(continuedMarker({ continuedFrom: { jobId: "j" } }), undefined, "a marker without a service is none");
eq(continuedMarker({ continuedFrom: "conn" }), undefined, "a marker that is not an object is none");
eq(continuedMarker({ text: "continue" }), undefined, "text that says continue is not a marker");
eq(continuedMarker(null), undefined, "no payload");
console.log("continued: ok");
