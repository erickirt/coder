import * as path from "node:path";
import { defineConfig } from "@playwright/test";
import {
	coderPort,
	coderdPProfPort,
	e2eFakeExperiment1,
	e2eFakeExperiment2,
	gitAuth,
	requireTerraformTests,
} from "./constants";

export const wsEndpoint = process.env.CODER_E2E_WS_ENDPOINT;
export const retries = (() => {
	if (process.env.CODER_E2E_TEST_RETRIES === undefined) {
		return undefined;
	}
	const count = Number.parseInt(process.env.CODER_E2E_TEST_RETRIES, 10);
	if (Number.isNaN(count)) {
		throw new Error(
			`CODER_E2E_TEST_RETRIES is not a number: ${process.env.CODER_E2E_TEST_RETRIES}`,
		);
	}
	if (count < 0) {
		throw new Error(
			`CODER_E2E_TEST_RETRIES is less than 0: ${process.env.CODER_E2E_TEST_RETRIES}`,
		);
	}
	return count;
})();

const localURL = (port: number, path: string): string => {
	return `http://localhost:${port}${path}`;
};

export default defineConfig({
	retries,
	globalSetup: require.resolve("./setup/preflight"),
	projects: [
		{
			name: "testsSetup",
			testMatch: /setup\/.*\.spec\.ts/,
		},
		{
			name: "tests",
			testMatch: /tests\/.*\.spec\.ts/,
			dependencies: ["testsSetup"],
			timeout: 30_000,
		},
	],
	reporter: [["./reporter.ts"]],
	use: {
		actionTimeout: 5000,
		baseURL: `http://localhost:${coderPort}`,
		video: "retain-on-failure",
		...(wsEndpoint
			? {
					connectOptions: {
						wsEndpoint: wsEndpoint,
					},
				}
			: {
					launchOptions: {
						args: ["--disable-webgl"],
					},
				}),
	},
	webServer: {
		url: `http://localhost:${coderPort}/api/v2/deployment/config`,
		command: [
			`go run -tags embed ${path.join(__dirname, "../../enterprise/cmd/coder")}`,
			"server",
			"--global-config $(mktemp -d -t e2e-XXXXXXXXXX)",
			`--access-url=http://localhost:${coderPort}`,
			`--http-address=0.0.0.0:${coderPort}`,
			"--ephemeral",
			"--telemetry=false",
			"--dangerous-disable-rate-limits",
			"--provisioner-daemons 10",
			// TODO: Enable some terraform provisioners
			`--provisioner-types=echo${requireTerraformTests ? ",terraform" : ""}`,
			"--provisioner-daemons=10",
			"--web-terminal-renderer=dom",
			"--pprof-enable",
		]
			.filter(Boolean)
			.join(" "),
		env: {
			...process.env,
			// Otherwise, the runner fails on Mac with: could not determine kind of name for C.uuid_string_t
			CGO_ENABLED: "0",

			// This is the test provider for git auth with devices!
			CODER_GITAUTH_0_ID: gitAuth.deviceProvider,
			CODER_GITAUTH_0_TYPE: "github",
			CODER_GITAUTH_0_CLIENT_ID: "client",
			CODER_GITAUTH_0_CLIENT_SECRET: "secret",
			CODER_GITAUTH_0_DEVICE_FLOW: "true",
			CODER_GITAUTH_0_APP_INSTALL_URL:
				"https://github.com/apps/coder/installations/new",
			CODER_GITAUTH_0_APP_INSTALLATIONS_URL: localURL(
				gitAuth.devicePort,
				gitAuth.installationsPath,
			),
			CODER_GITAUTH_0_TOKEN_URL: localURL(
				gitAuth.devicePort,
				gitAuth.tokenPath,
			),
			CODER_GITAUTH_0_DEVICE_CODE_URL: localURL(
				gitAuth.devicePort,
				gitAuth.codePath,
			),
			CODER_GITAUTH_0_VALIDATE_URL: localURL(
				gitAuth.devicePort,
				gitAuth.validatePath,
			),

			CODER_GITAUTH_1_ID: gitAuth.webProvider,
			CODER_GITAUTH_1_TYPE: "github",
			CODER_GITAUTH_1_CLIENT_ID: "client",
			CODER_GITAUTH_1_CLIENT_SECRET: "secret",
			CODER_GITAUTH_1_AUTH_URL: localURL(gitAuth.webPort, gitAuth.authPath),
			CODER_GITAUTH_1_TOKEN_URL: localURL(gitAuth.webPort, gitAuth.tokenPath),
			CODER_GITAUTH_1_DEVICE_CODE_URL: localURL(
				gitAuth.webPort,
				gitAuth.codePath,
			),
			CODER_GITAUTH_1_VALIDATE_URL: localURL(
				gitAuth.webPort,
				gitAuth.validatePath,
			),
			CODER_PPROF_ADDRESS: `127.0.0.1:${coderdPProfPort}`,
			CODER_EXPERIMENTS: `${e2eFakeExperiment1},${e2eFakeExperiment2}`,

			// Tests for Deployment / User Authentication / OIDC
			CODER_OIDC_ISSUER_URL: "https://accounts.google.com",
			CODER_OIDC_EMAIL_DOMAIN: "coder.com",
			CODER_OIDC_CLIENT_ID: "1234567890",
			CODER_OIDC_CLIENT_SECRET: "1234567890Secret",
			CODER_OIDC_ALLOW_SIGNUPS: "false",
			CODER_OIDC_SIGN_IN_TEXT: "Hello",
			CODER_OIDC_ICON_URL: "/icon/google.svg",
		},
		reuseExistingServer: false,
	},
});
