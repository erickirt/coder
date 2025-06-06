import { workspaceBuildParameters } from "api/queries/workspaceBuilds";
import type { Workspace, WorkspaceBuildParameter } from "api/typesGenerated";
import { linkToTemplate, useLinks } from "modules/navigation";
import { useCallback } from "react";
import { useQuery } from "react-query";
import { useNavigate } from "react-router-dom";
import type { CreateWorkspaceMode } from "../../../pages/CreateWorkspacePage/CreateWorkspacePage";

function getDuplicationUrlParams(
	workspaceParams: readonly WorkspaceBuildParameter[],
	workspace: Workspace,
): URLSearchParams {
	// Record type makes sure that every property key added starts with "param.";
	// page is also set up to parse params with this prefix for auto mode
	const consolidatedParams: Record<`param.${string}`, string> = {};

	for (const p of workspaceParams) {
		consolidatedParams[`param.${p.name}`] = p.value;
	}

	return new URLSearchParams({
		...consolidatedParams,
		mode: "duplicate" satisfies CreateWorkspaceMode,
		name: `${workspace.name}-copy`,
		version: workspace.template_active_version_id,
	});
}

/**
 * Takes a workspace, and returns out a function that will navigate the user to
 * the 'Create Workspace' page, pre-filling the form with as much information
 * about the workspace as possible.
 */
export function useWorkspaceDuplication(workspace?: Workspace) {
	const navigate = useNavigate();
	const getLink = useLinks();
	const buildParametersQuery = useQuery({
		...workspaceBuildParameters(workspace?.latest_build.id ?? ""),
		enabled: !!workspace,
	});

	// Not using useEffectEvent for this, because useEffect isn't really an
	// intended use case for this custom hook
	const duplicateWorkspace = useCallback(() => {
		const buildParams = buildParametersQuery.data;
		if (buildParams === undefined || workspace === undefined) {
			return;
		}

		const newUrlParams = getDuplicationUrlParams(buildParams, workspace);

		// Necessary for giving modals/popups time to flush their state changes and
		// close the popup before actually navigating. MUI does provide the
		// disablePortal prop, which also side-steps this issue, but you have to
		// remember to put it on any component that calls this function. Better to
		// code defensively and have some redundancy in case someone forgets
		void Promise.resolve().then(() => {
			navigate({
				pathname: `${getLink(
					linkToTemplate(workspace.organization_name, workspace.template_name),
				)}/workspace`,
				search: newUrlParams.toString(),
			});
		});
	}, [getLink, navigate, workspace, buildParametersQuery.data]);

	return {
		duplicateWorkspace,
		isDuplicationReady: buildParametersQuery.isSuccess,
	} as const;
}
