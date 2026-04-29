import {
	BaseEdge,
	EdgeLabelRenderer,
	type EdgeProps,
	getStraightPath,
} from "reactflow";
import type { CSSProperties } from "react";

const NODE_WIDTH = 160;
const ENDPOINT_LABEL_DISTANCE = NODE_WIDTH / 2;

export type InterfaceLabelEdgeData = {
	interfaceAId?: string;
	interfaceAName: string;
	interfaceAIP?: string;
	interfaceBId?: string;
	interfaceBName: string;
	interfaceBIP?: string;
};

function endpointLabelStyle(x: number, y: number): CSSProperties {
	return {
		position: "absolute",
		transform: `translate(-50%, -50%) translate(${x}px, ${y}px)`,
	};
}

function pointAlongLine(
	sourceX: number,
	sourceY: number,
	targetX: number,
	targetY: number,
	distance: number,
	from: "source" | "target",
) {
	const dx = targetX - sourceX;
	const dy = targetY - sourceY;
	const length = Math.hypot(dx, dy);

	if (length === 0) {
		return { x: sourceX, y: sourceY };
	}

	const ux = dx / length;
	const uy = dy / length;

	if (from === "source") {
		return {
			x: sourceX + ux * distance,
			y: sourceY + uy * distance,
		};
	}

	return {
		x: targetX - ux * distance,
		y: targetY - uy * distance,
	};
}

export function InterfaceLabelEdge({
	id,
	sourceX,
	sourceY,
	targetX,
	targetY,
	style,
	markerEnd,
	data,
}: EdgeProps<InterfaceLabelEdgeData>) {
	const adjustedSourceY = sourceY + 8;
	const adjustedTargetY = targetY + 8;
	const [edgePath] = getStraightPath({
		sourceX,
		sourceY: adjustedSourceY,
		targetX,
		targetY: adjustedTargetY,
	});
	const sourceLabelPoint = pointAlongLine(
		sourceX,
		adjustedSourceY,
		targetX,
		adjustedTargetY,
		ENDPOINT_LABEL_DISTANCE,
		"source",
	);
	const targetLabelPoint = pointAlongLine(
		sourceX,
		adjustedSourceY,
		targetX,
		adjustedTargetY,
		ENDPOINT_LABEL_DISTANCE,
		"target",
	);

	return (
		<>
			<BaseEdge id={id} path={edgePath} style={style} markerEnd={markerEnd} />
			<EdgeLabelRenderer>
				<div
					style={endpointLabelStyle(sourceLabelPoint.x, sourceLabelPoint.y)}
					className="pointer-events-none z-0 rounded border border-slate-300 bg-white/90 px-1.5 py-0.5 font-mono text-[10px] text-slate-700 shadow-sm"
				>
					<div>{data?.interfaceAName}</div>
					{data?.interfaceAIP ? <div className="text-[9px] text-slate-500">{data.interfaceAIP}</div> : null}
				</div>
				<div
					style={endpointLabelStyle(targetLabelPoint.x, targetLabelPoint.y)}
					className="pointer-events-none z-0 rounded border border-slate-300 bg-white/90 px-1.5 py-0.5 font-mono text-[10px] text-slate-700 shadow-sm"
				>
					<div>{data?.interfaceBName}</div>
					{data?.interfaceBIP ? <div className="text-[9px] text-slate-500">{data.interfaceBIP}</div> : null}
				</div>
			</EdgeLabelRenderer>
		</>
	);
}
