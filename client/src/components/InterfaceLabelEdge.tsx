import {
	BaseEdge,
	EdgeLabelRenderer,
	type EdgeProps,
	getStraightPath,
} from "reactflow";
import type { CSSProperties } from "react";

const NODE_WIDTH = 160;
const NODE_HEIGHT = 118;
const LABEL_GAP = 6;

export type InterfaceLabelEdgeData = {
	interfaceAId?: string;
	interfaceAName: string;
	interfaceAIP?: string;
	interfaceBId?: string;
	interfaceBName: string;
	interfaceBIP?: string;
	flowAToB?: boolean;
	flowBToA?: boolean;
	flowReduced?: boolean;
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

function distanceToNodeEdge(dx: number, dy: number): number {
	const halfW = NODE_WIDTH / 2;
	const halfH = NODE_HEIGHT / 2;
	const adx = Math.abs(dx);
	const ady = Math.abs(dy);
	if (adx === 0 && ady === 0) return halfW + LABEL_GAP;
	const tx = adx === 0 ? Number.POSITIVE_INFINITY : halfW / adx;
	const ty = ady === 0 ? Number.POSITIVE_INFINITY : halfH / ady;
	return Math.min(tx, ty) * Math.hypot(dx, dy) + LABEL_GAP;
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
	const [edgePath] = getStraightPath({ sourceX, sourceY, targetX, targetY });
	const [reverseEdgePath] = getStraightPath({ sourceX: targetX, sourceY: targetY, targetX: sourceX, targetY: sourceY });
	const dx = targetX - sourceX;
	const dy = targetY - sourceY;
	const edgeDistance = distanceToNodeEdge(dx, dy);
	const sourceLabelPoint = pointAlongLine(sourceX, sourceY, targetX, targetY, edgeDistance, "source");
	const targetLabelPoint = pointAlongLine(sourceX, sourceY, targetX, targetY, edgeDistance, "target");
	const arrowCount = data?.flowReduced ? 2 : 7;
	const arrowDurationSec = 2;
	const arrowPoints = "-6,-4 6,0 -6,4 -2,0";

	return (
		<>
			<BaseEdge id={id} path={edgePath} style={style} markerEnd={markerEnd} />
			{data?.flowAToB ? (
				<g>
					<path d={edgePath} stroke="none" fill="none" />
					{Array.from({ length: arrowCount }).map((_, index) => (
						<polygon
							key={`ab-${id}-${index}`}
							points={arrowPoints}
							fill="#22c55e"
							stroke="#ffffff"
							strokeWidth="1.1"
						>
							<animateMotion
								dur={`${arrowDurationSec}s`}
								begin={`${-(arrowDurationSec / arrowCount) * index}s`}
								repeatCount="indefinite"
								rotate="auto"
								path={edgePath}
							/>
						</polygon>
					))}
				</g>
			) : null}
			{data?.flowBToA ? (
				<g>
					<path d={reverseEdgePath} stroke="none" fill="none" />
					{Array.from({ length: arrowCount }).map((_, index) => (
						<polygon
							key={`ba-${id}-${index}`}
							points={arrowPoints}
							fill="#16a34a"
							stroke="#ffffff"
							strokeWidth="1.1"
						>
							<animateMotion
								dur={`${arrowDurationSec}s`}
								begin={`${-(arrowDurationSec / arrowCount) * index}s`}
								repeatCount="indefinite"
								rotate="auto"
								path={reverseEdgePath}
							/>
						</polygon>
					))}
				</g>
			) : null}
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
