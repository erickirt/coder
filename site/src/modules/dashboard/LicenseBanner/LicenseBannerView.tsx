import {
	type CSSObject,
	type Interpolation,
	type Theme,
	css,
	useTheme,
} from "@emotion/react";
import Link from "@mui/material/Link";
import { LicenseTelemetryRequiredErrorText } from "api/typesGenerated";
import { Expander } from "components/Expander/Expander";
import { Pill } from "components/Pill/Pill";
import { type FC, useState } from "react";

const Language = {
	licenseIssue: "License Issue",
	licenseIssues: (num: number): string => `${num} License Issues`,
	upgrade: "Contact sales@coder.com.",
	exception: "Contact sales@coder.com if you need an exception.",
	exceeded: "It looks like you've exceeded some limits of your license.",
	lessDetails: "Less",
	moreDetails: "More",
};

const styles = {
	leftContent: {
		marginRight: 8,
		marginLeft: 8,
	},
} satisfies Record<string, Interpolation<Theme>>;

const formatMessage = (message: string) => {
	// If the message ends with an alphanumeric character, add a period.
	if (/[a-z0-9]$/i.test(message)) {
		return `${message}.`;
	}
	return message;
};

interface LicenseBannerViewProps {
	errors: readonly string[];
	warnings: readonly string[];
}

export const LicenseBannerView: FC<LicenseBannerViewProps> = ({
	errors,
	warnings,
}) => {
	const theme = useTheme();
	const [showDetails, setShowDetails] = useState(false);
	const isError = errors.length > 0;
	const messages = [...errors, ...warnings];
	const type = isError ? "error" : "warning";

	const containerStyles = css`
    ${theme.typography.body2 as CSSObject}

    display: flex;
    align-items: center;
    padding: 12px;
    background-color: ${theme.roles[type].background};
  `;

	const textColor = theme.roles[type].text;

	if (messages.length === 1) {
		return (
			<div css={containerStyles}>
				<Pill type={type}>{Language.licenseIssue}</Pill>
				<div css={styles.leftContent}>
					<span>{formatMessage(messages[0])}</span>
					&nbsp;
					<Link
						color={textColor}
						fontWeight="medium"
						href="mailto:sales@coder.com"
					>
						{messages[0] === LicenseTelemetryRequiredErrorText
							? Language.exception
							: Language.upgrade}
					</Link>
				</div>
			</div>
		);
	}

	return (
		<div css={containerStyles}>
			<Pill type={type}>{Language.licenseIssues(messages.length)}</Pill>
			<div css={styles.leftContent}>
				<div>
					{Language.exceeded}
					&nbsp;
					<Link
						color={textColor}
						fontWeight="medium"
						href="mailto:sales@coder.com"
					>
						{Language.upgrade}
					</Link>
				</div>
				<Expander expanded={showDetails} setExpanded={setShowDetails}>
					<ul css={{ padding: 8, margin: 0 }}>
						{messages.map((message) => (
							<li css={{ margin: 4 }} key={message}>
								{formatMessage(message)}
							</li>
						))}
					</ul>
				</Expander>
			</div>
		</div>
	);
};
