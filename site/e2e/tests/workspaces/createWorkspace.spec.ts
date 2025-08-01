import { expect, test } from "@playwright/test";
import { users } from "../../constants";
import {
	StarterTemplates,
	createTemplate,
	createWorkspace,
	disableDynamicParameters,
	echoResponsesWithParameters,
	login,
	openTerminalWindow,
	requireTerraformProvisioner,
	verifyParameters,
} from "../../helpers";
import { beforeCoderTest } from "../../hooks";
import {
	fifthParameter,
	firstParameter,
	fourthParameter,
	randParamName,
	secondParameter,
	seventhParameter,
	sixthParameter,
	thirdParameter,
} from "../../parameters";
import type { RichParameter } from "../../provisionerGenerated";

test.describe.configure({ mode: "parallel" });

test.beforeEach(async ({ page }) => {
	beforeCoderTest(page);
});

test("create workspace", async ({ page }) => {
	await login(page, users.templateAdmin);
	const template = await createTemplate(page, {
		apply: [{ apply: { resources: [{ name: "example" }] } }],
	});

	// Disable dynamic parameters to use classic parameter flow for this test
	await disableDynamicParameters(page, template);

	await login(page, users.member);
	await createWorkspace(page, template);
});

test("create workspace with default immutable parameters", async ({ page }) => {
	await login(page, users.templateAdmin);
	const richParameters: RichParameter[] = [
		secondParameter,
		fourthParameter,
		fifthParameter,
	];
	const template = await createTemplate(
		page,
		echoResponsesWithParameters(richParameters),
	);

	// Disable dynamic parameters to use classic parameter flow for this test
	await disableDynamicParameters(page, template);

	await login(page, users.member);
	const workspaceName = await createWorkspace(page, template);
	await verifyParameters(page, workspaceName, richParameters, [
		{ name: secondParameter.name, value: secondParameter.defaultValue },
		{ name: fourthParameter.name, value: fourthParameter.defaultValue },
		{ name: fifthParameter.name, value: fifthParameter.defaultValue },
	]);
});

test("create workspace with default mutable parameters", async ({ page }) => {
	await login(page, users.templateAdmin);
	const richParameters: RichParameter[] = [firstParameter, thirdParameter];
	const template = await createTemplate(
		page,
		echoResponsesWithParameters(richParameters),
	);

	// Disable dynamic parameters to use classic parameter flow for this test
	await disableDynamicParameters(page, template);

	await login(page, users.member);
	const workspaceName = await createWorkspace(page, template);
	await verifyParameters(page, workspaceName, richParameters, [
		{ name: firstParameter.name, value: firstParameter.defaultValue },
		{ name: thirdParameter.name, value: thirdParameter.defaultValue },
	]);
});

test("create workspace with default and required parameters", async ({
	page,
}) => {
	await login(page, users.templateAdmin);
	const richParameters: RichParameter[] = [
		secondParameter,
		fourthParameter,
		sixthParameter,
		seventhParameter,
	];
	const buildParameters = [
		{ name: sixthParameter.name, value: "12345" },
		{ name: seventhParameter.name, value: "abcdef" },
	];
	const template = await createTemplate(
		page,
		echoResponsesWithParameters(richParameters),
	);

	// Disable dynamic parameters to use classic parameter flow for this test
	await disableDynamicParameters(page, template);

	await login(page, users.member);
	const workspaceName = await createWorkspace(page, template, {
		richParameters,
		buildParameters,
	});
	await verifyParameters(page, workspaceName, richParameters, [
		// user values:
		...buildParameters,
		// default values:
		{ name: secondParameter.name, value: secondParameter.defaultValue },
		{ name: fourthParameter.name, value: fourthParameter.defaultValue },
	]);
});

test("create workspace and overwrite default parameters", async ({ page }) => {
	await login(page, users.templateAdmin);
	// We use randParamName to prevent the new values from corrupting user_history
	// and thus affecting other tests.
	const richParameters: RichParameter[] = [
		randParamName(secondParameter),
		randParamName(fourthParameter),
	];

	const buildParameters = [
		{ name: richParameters[0].name, value: "AAAAA" },
		{ name: richParameters[1].name, value: "false" },
	];
	const template = await createTemplate(
		page,
		echoResponsesWithParameters(richParameters),
	);

	// Disable dynamic parameters to use classic parameter flow for this test
	await disableDynamicParameters(page, template);

	await login(page, users.member);
	const workspaceName = await createWorkspace(page, template, {
		richParameters,
		buildParameters,
	});
	await verifyParameters(page, workspaceName, richParameters, buildParameters);
});

test("create workspace with disable_param search params", async ({ page }) => {
	await login(page, users.templateAdmin);
	const richParameters: RichParameter[] = [
		firstParameter, // mutable
		secondParameter, //immutable
	];

	const templateName = await createTemplate(
		page,
		echoResponsesWithParameters(richParameters),
	);

	// Disable dynamic parameters to use classic parameter flow for this test
	await disableDynamicParameters(page, templateName);

	await login(page, users.member);
	await page.goto(
		`/templates/${templateName}/workspace?disable_params=first_parameter,second_parameter`,
		{ waitUntil: "domcontentloaded" },
	);

	await expect(page.getByLabel(/First parameter/i)).toBeDisabled();
	await expect(page.getByLabel(/Second parameter/i)).toBeDisabled();
});

// Creating docker containers is currently leaky. They are not cleaned up when
// the tests are over.
test.skip("create docker workspace", async ({ context, page }) => {
	requireTerraformProvisioner();

	await login(page, users.templateAdmin);
	const template = await createTemplate(page, StarterTemplates.STARTER_DOCKER);

	// Disable dynamic parameters to use classic parameter flow for this test
	await disableDynamicParameters(page, template);

	await login(page, users.member);
	const workspaceName = await createWorkspace(page, template);

	// The workspace agents must be ready before we try to interact with the workspace.
	await page.waitForSelector(
		`//div[@role="status"][@data-testid="agent-status-ready"]`,
		{ state: "visible" },
	);

	// Wait for the terminal button to be visible, and click it.
	const terminalButton =
		"//a[@data-testid='terminal'][normalize-space()='Terminal']";
	await page.waitForSelector(terminalButton, {
		state: "visible",
	});

	const terminal = await openTerminalWindow(
		page,
		context,
		workspaceName,
		"main",
	);
	await terminal.waitForSelector(
		`//textarea[contains(@class,"xterm-helper-textarea")]`,
		{ state: "visible" },
	);
});
