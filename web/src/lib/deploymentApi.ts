import { req } from "./api.ts";
import type { DeploymentVersions } from "./deployment.ts";

// Deployment build identity (R2), super-admin.
export const deploymentApi = {
  versions: () => req<DeploymentVersions>("/v1/platform/versions"),
};
