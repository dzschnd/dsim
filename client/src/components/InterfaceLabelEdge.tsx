import {
	BaseEdge,
	EdgeLabelRenderer,
	type EdgeProps,
	getStraightPath,
} from "reactflow";
import type { CSSProperties } from "react";

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
	const sourceLabelX = sourceX + (targetX - sourceX) * 0.18;
	const sourceLabelY = sourceY + (targetY - sourceY) * 0.18;
	const targetLabelX = sourceX + (targetX - sourceX) * 0.82;
	const targetLabelY = sourceY + (targetY - sourceY) * 0.82;

	return (
		<>
			<BaseEdge id={id} path={edgePath} style={style} markerEnd={markerEnd} />
			<EdgeLabelRenderer>
				<div
					style={endpointLabelStyle(sourceLabelX, sourceLabelY)}
					className="pointer-events-none rounded border border-slate-300 bg-white/90 px-1.5 py-0.5 font-mono text-[10px] text-slate-700 shadow-sm"
				>
					<div>{data?.interfaceAName}</div>
					{data?.interfaceAIP ? <div className="text-[9px] text-slate-500">{data.interfaceAIP}</div> : null}
				</div>
				<div
					style={endpointLabelStyle(targetLabelX, targetLabelY)}
					className="pointer-events-none rounded border border-slate-300 bg-white/90 px-1.5 py-0.5 font-mono text-[10px] text-slate-700 shadow-sm"
				>
					<div>{data?.interfaceBName}</div>
					{data?.interfaceBIP ? <div className="text-[9px] text-slate-500">{data.interfaceBIP}</div> : null}
				</div>
			</EdgeLabelRenderer>
		</>
	);
}
