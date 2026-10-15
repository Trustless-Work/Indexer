// Railway Infrastructure as Code for the Indexer service. Replaces the
// deprecated railway.json (Config as Code stops being read on 2026-12-01).
//
// Railway does NOT read this file on deploy: a push to main builds and
// deploys with whatever settings the service already holds. This file only
// changes them when an operator runs `railway config plan` / `apply`
// (procedure and pre-flight checks: docs/railway-deployment.md, section 6).
import { defineRailway, project, service } from "railway/iac";

// Other services in the Railway project (the RabbitMQ broker) are not declared
// here. A named partial manages only what this file declares, so leaving them
// out never plans their deletion.
export const partial = "Indexer";

export default defineRailway((ctx) => {
  const indexer = service("Indexer", {
    build: { builder: "DOCKERFILE" },
    deploy: {
      restartPolicyType: "ALWAYS",
      restartPolicyMaxRetries: 10,
    },
    // Never more than one: two live indexers on one state volume is forbidden
    // (the ingest loop is the single writer of the cursor and the watchlist).
    replicas: 1,
  });

  return project(ctx.projectName ?? "trustless-work", {
    // Variables stay owned by the Railway dashboard. With the default
    // (managed) policy this file would own every variable on the service, and
    // the secrets and STATE_PATH would have to be declared here.
    variables: { managed: false },
    resources: [indexer],
  });
});
