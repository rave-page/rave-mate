// framecmp.hlsl - exact new-frame detector (cap.zig FrameCmp). Full-screen triangle; the pixel shader
// DISCARDS every pixel where the two textures are bit-equal, so an occlusion query around the draw
// counts CHANGED pixels. No render target is bound: the result is the query, never a pixel.
// Load (texel-exact, no sampler, no filtering) of a UNORM texel is exact, so == is a byte compare.
//
// Bytecode is committed (framecmp_vs.dxbc / framecmp_ps.dxbc, embedded by cap.zig) - no runtime
// d3dcompiler_47.dll. Regenerate after an edit (Windows SDK fxc, SM 4.0 = feature level 10.0):
//   fxc /nologo /T vs_4_0 /E vs /O3 /Fo framecmp_vs.dxbc framecmp.hlsl
//   fxc /nologo /T ps_4_0 /E ps /O3 /Fo framecmp_ps.dxbc framecmp.hlsl

Texture2D<float4> cur : register(t0);
Texture2D<float4> prev : register(t1);

float4 vs(uint id : SV_VertexID) : SV_Position {
    float2 uv = float2((id << 1) & 2, id & 2);
    return float4(uv * float2(2, -2) + float2(-1, 1), 0, 1);
}

void ps(float4 p : SV_Position) {
    int3 c = int3(p.xy, 0);
    if (all(cur.Load(c) == prev.Load(c))) discard;
}
