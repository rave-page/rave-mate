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
