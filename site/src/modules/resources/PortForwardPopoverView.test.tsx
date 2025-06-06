import { screen } from "@testing-library/react";
import { QueryClientProvider } from "react-query";
import {
	MockListeningPortsResponse,
	MockTemplate,
	MockWorkspace,
	MockWorkspaceAgent,
} from "testHelpers/entities";
import {
	createTestQueryClient,
	renderComponent,
} from "testHelpers/renderHelpers";
import { PortForwardPopoverView } from "./PortForwardButton";

describe("Port Forward Popover View", () => {
	it("renders component", async () => {
		renderComponent(
			<QueryClientProvider client={createTestQueryClient()}>
				<PortForwardPopoverView
					agent={MockWorkspaceAgent}
					template={MockTemplate}
					listeningPorts={MockListeningPortsResponse.ports}
					portSharingControlsEnabled
					host="host"
					workspace={MockWorkspace}
					sharedPorts={[]}
					refetchSharedPorts={jest.fn()}
				/>
			</QueryClientProvider>,
		);

		expect(
			screen.getByText(MockListeningPortsResponse.ports[0].port),
		).toBeInTheDocument();

		expect(
			screen.getByText(MockListeningPortsResponse.ports[0].process_name),
		).toBeInTheDocument();
	});
});
