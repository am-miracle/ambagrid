import { createFileRoute } from "@tanstack/react-router";
import { AssetPage } from "#/features/asset/asset-page";

export const Route = createFileRoute("/assets/$assetId")({
	component: AssetRoute,
});

function AssetRoute() {
	const { assetId } = Route.useParams();
	return <AssetPage key={assetId} assetId={assetId} />;
}
