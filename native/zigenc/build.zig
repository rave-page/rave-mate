const std = @import("std");

// rave-mate-enc: per-adapter Media Foundation hardware H.264 encoder child (Zig, no cgo).
// Spawned + supervised by the media child; sessions multiplex over stdio JSON control +
// per-session shared-memory rings. Windows-only (Media Foundation).
//   zig build -Drelease [-Dtarget=x86_64-windows-gnu]
// Artifact: zig-out/bin/rave-mate-enc.exe
pub fn build(b: *std.Build) void {
    const target = b.standardTargetOptions(.{});
    const optimize = b.standardOptimizeOption(.{ .preferred_optimize_mode = .ReleaseFast });

    if (target.result.os.tag != .windows) return; // MF is Windows-only

    const mod = b.createModule(.{
        .root_source_file = b.path("src/main.zig"),
        .target = target,
        .optimize = optimize,
    });
    // Shared D3D11/DXGI declarations (native/zigd3d) - the SAME module native/zigui's shell child
    // imports for render surfaces. b.path() cannot leave the package root, so the sibling package is
    // addressed off the absolute build root.
    mod.addImport("d3d11", b.createModule(.{
        .root_source_file = .{ .cwd_relative = b.pathJoin(&.{ b.build_root.path.?, "..", "zigd3d", "src", "d3d11.zig" }) },
        .target = target,
        .optimize = optimize,
    }));
    mod.linkSystemLibrary("mfplat", .{});
    mod.linkSystemLibrary("d3d11", .{});
    mod.linkSystemLibrary("dxgi", .{});
    mod.linkSystemLibrary("ole32", .{});
    const exe = b.addExecutable(.{ .name = "rave-mate-enc", .root_module = mod });
    exe.subsystem = .Console; // stdio control plane; parent hides the window
    b.installArtifact(exe);

    const tests = b.addTest(.{ .root_module = mod });
    const run_tests = b.addRunArtifact(tests);
    const test_step = b.step("test", "Run unit tests");
    test_step.dependOn(&run_tests.step);
}
