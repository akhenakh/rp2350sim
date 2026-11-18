import subprocess, tempfile, os, struct

SNIPPETS = [
# name, asm
("adds_rr","adds r0, r1, r2"),
("adds_ri","adds r0, r1, #7"),
("subs_rr","subs r0, r1, r2"),
("subs_ri","subs r0, r1, #7"),
("movs_imm","movs r0, #0x7f"),
("cmp_rr","cmp r0, r1"),
("cmn_rr","cmn r0, r1"),
("and_rr","ands r0, r1"),
("orr_rr","orrs r0, r1"),
("eor_rr","eors r0, r1"),
("bic_rr","bics r0, r1"),
("mvn_rr","mvns r0, r1"),
("adc_rr","adcs r0, r1"),
("sbc_rr","sbcs r0, r1"),
("rsb_zero","rsbs r0, r1, #0"),
("mul16","muls r0, r1, r0"),
("lsl_imm","lsls r0, r1, #5"),
("lsr_imm","lsrs r0, r1, #5"),
("asr_imm","asrs r0, r1, #5"),
("lsr32","lsrs r0, r1, #32"),
("asr32","asrs r0, r1, #32"),
("lsl_reg","lsls r0, r1, r2"),
("lsr_reg","lsrs r0, r1, r2"),
("asr_reg","asrs r0, r1, r2"),
("ror_reg","rors r0, r1, r2"),
("sxth","sxth r0, r1"),
("sxtb","sxtb r0, r1"),
("uxth","uxth r0, r1"),
("uxtb","uxtb r0, r1"),
("rev","rev r0, r1"),
("rev16","rev16 r0, r1"),
("revsh","revsh r0, r1"),
("ldr16_imm","ldr r0, [r3, #8]"),
("ldr16_reg","ldr r0, [r3, r1]"),
("str16_imm","str r0, [r3, #8]"),
("ldrb16","ldrb r0, [r3, #1]"),
("ldrh16","ldrh r0, [r3, #2]"),
("strb16","strb r0, [r3]"),
("strh16","strh r0, [r3]"),
("ldr_sp","ldr r0, [sp, #8]"),
("str_sp","str r0, [sp, #8]"),
("sub_sp16","sub sp, #16\n add sp, #16"),
("sub_sp4","sub sp, #4\n add sp, #4"),
("add_sp508","add sp, #508\n sub sp, #508"),
("add_sp_rd","add r0, sp, #8"),
("adr16","add r0, pc, #8"),
("cbz16","cbz r0, 1f\n nop\n1:"),
("cbnz16","cbnz r0, 1f\n nop\n1:"),
("b16","b 1f\n nop\n1:"),
("beq16","beq 1f\n nop\n1:"),
("push16","push {r0, r1, r2, r3}"),
("ldm16","ldmia r3!, {r0, r1}"),
("stm16","stmia r3!, {r0, r1}"),
# 32-bit
("mov_w","mov.w r0, r1"),
("mov_w_imm","mov.w r0, #0xff000000"),
("movw","movw r0, #0x1234"),
("movt","movt r0, #0xabcd"),
("add_w","add.w r0, r1, r2"),
("add_w_shift","add.w r0, r1, r2, lsl #3"),
("adds_w","adds.w r0, r1, r2"),
("add_w_imm","add.w r0, r1, #0xff"),
("sub_w_imm","sub.w r0, r1, #0xff"),
("rsb_w_imm","rsb.w r0, r1, #0xff"),
("and_w","and.w r0, r1, r2"),
("orr_w","orr.w r0, r1, r2"),
("eor_w","eor.w r0, r1, r2"),
("bic_w","bic.w r0, r1, r2"),
("orn_w","orn.w r0, r1, r2"),
("mvn_w","mvn.w r0, r1"),
("cmp_w","cmp.w r0, r1"),
("cmn_w","cmn.w r0, r1"),
("tst_w","tst.w r0, r1"),
("teq_w","teq.w r0, r1"),
("adc_w","adc.w r0, r1, r2"),
("sbc_w","sbc.w r0, r1, r2"),
("and_w_imm","and.w r0, r1, #0xff"),
("orr_w_imm","orr.w r0, r1, #0xff00ff00"),
("eor_w_imm","eor.w r0, r1, #0xff"),
("bic_w_imm","bic.w r0, r1, #0xff"),
("add_w_imm2","add.w r0, r1, #0x8000"),
("orr_w_lsl","orr.w r0, r1, r2, lsl #3"),
("add_w_lsr31","add.w r0, r1, r2, lsr #31"),
("sub_w_asr","sub.w r0, r1, r2, asr #4"),
("and_w_ror","and.w r0, r1, r2, ror #7"),
("eor_w_lsl0","eor.w r0, r1, r2, lsl #0"),
("bic_w_lsl16","bic.w r0, r1, r2, lsl #16"),
("rsb_w_lsl","rsb.w r0, r1, r2, lsl #2"),
("mvn_w_lsr","mvn.w r0, r1, lsr #5"),
("movs_w_shift","movs.w r0, r1, lsl #3"),
("lsl_w","lsl.w r0, r1, r2"),
("lsr_w","lsr.w r0, r1, r2"),
("asr_w","asr.w r0, r1, r2"),
("ror_w","ror.w r0, r1, r2"),
("clz","clz r0, r1"),
("rbit","rbit r0, r1"),
("rev_w","rev.w r0, r1"),
("rev16_w","rev16.w r0, r1"),
("revsh_w","revsh.w r0, r1"),
("sxtb_w","sxtb.w r0, r1"),
("sxth_w","sxth.w r0, r1"),
("uxtb_w","uxtb.w r0, r1"),
("uxth_w","uxth.w r0, r1"),
("udiv","udiv r0, r1, r2"),
("sdiv","sdiv r0, r1, r2"),
("mul_w","mul r0, r1, r2"),
("mla","mla r0, r1, r2, r3"),
("mls","mls r0, r1, r2, r3"),
("umull","umull r0, r1, r2, r3"),
("umlal","umlal r0, r1, r2, r3"),
("smull","smull r0, r1, r2, r3"),
("smlal","smlal r0, r1, r2, r3"),
("bfi","bfi r0, r1, #4, #8"),
("bfc","bfc r0, #4, #8"),
("ubfx","ubfx r0, r1, #4, #8"),
("sbfx","sbfx r0, r1, #4, #8"),
("b_w","b 1f\n nop\n1:"),
("bl","bl 1f\n nop\n1:"),
("beq_w","beq 1f\n nop\n1:"),
("bhi_w","bhi 1f\n nop\n1:"),
("bls_w","bls 1f\n nop\n1:"),
("bge_w","bge 1f\n nop\n1:"),
("blt_w","blt 1f\n nop\n1:"),
("bgt_w","bgt 1f\n nop\n1:"),
("ble_w","ble 1f\n nop\n1:"),
("tbb","tbb [r3, r1]"),
("tbb_pc","tbb [pc, r1]\n .byte 4, 6, 8, 10\n nop\n nop\n nop\n nop\n nop\n nop"),
("tbh_pc","tbh [pc, r1, lsl #1]\n .short 4, 6\n nop\n nop\n nop\n nop"),
("tbh","tbh [r3, r1, lsl #1]"),
("ldr_w_imm","ldr.w r0, [r3, #0x24]"),
("str_w_imm","str.w r0, [r3, #0x24]"),
("ldrb_w","ldrb.w r0, [r3, #2]"),
("ldrh_w","ldrh.w r0, [r3, #2]"),
("ldrsb_w","ldrsb.w r0, [r3, #1]"),
("ldrsh_w","ldrsh.w r0, [r3, #2]"),
("ldr_w_reg","ldr.w r0, [r3, r1, lsl #2]"),
("str_w_reg","str.w r0, [r3, r1, lsl #2]"),
("ldr_w_post","ldr.w r0, [r3], #8"),
("ldr_w_pre","ldr.w r0, [r3, #8]!"),
("ldrd","ldrd r0, r1, [r3, #8]"),
("strd","strd r0, r1, [r3, #8]"),
("ldm_w","ldm.w r3, {r0, r1, r2}"),
("stm_w","stm.w r3, {r0, r1, r2}"),
("push_w","push.w {r0-r3}"),
("pop_w","pop.w {r0-r3}"),
("ldrex","ldrex r0, [r3]"),
("strex","strex r0, r1, [r3]"),
("ldaex","ldaex r0, [r3]"),
("stlex","stlex r0, r1, [r3]"),
("lda","lda r0, [r3]"),
("stl","stl r0, [r3]"),
("clrex","clrex"),
("mrs","mrs r0, primask"),
("msr","msr primask, r0"),
("dmb","dmb sy"),
("isb","isb sy"),
("addw","addw r0, r1, #0x123"),
("subw","subw r0, r1, #0x123"),
("it_eq","it eq\n moveq r0, r1"),
("it_ne","it ne\n movne r0, r1"),
("ite_eq","ite eq\n moveq r0, r1\n movne r2, r3"),
]

def asm_bytes(asm):
    src = ".syntax unified\n.thumb\n.cpu cortex-m33\n.text\n" + asm + "\n"
    with tempfile.TemporaryDirectory() as d:
        sp = os.path.join(d, "t.s")
        op = os.path.join(d, "t.o")
        bp = os.path.join(d, "t.bin")
        open(sp, "w").write(src)
        r = subprocess.run(["llvm-mc","-triple=thumbv8m.main","-filetype=obj",sp,"-o",op], capture_output=True)
        if r.returncode != 0:
            raise SystemExit("asm failed for %r: %s" % (asm, r.stderr.decode()))
        subprocess.run(["llvm-objcopy","-O","binary","--only-section=.text",op,bp], check=True)
        return open(bp,"rb").read()

cases = []
pats = [
    (0x00000001,0x00000002,0x00000003,0x80000000,0x0000001f),
    (0xFFFFFFFF,0x00000001,0x00000000,0x7FFFFFFF,0x00000020),
    (0x80000000,0x80000000,0x0000001F,0x00000004,0x00000021),
    (0x7FFFFFFF,0xFFFFFFFF,0x00000020,0x00000008,0x00000000),
]
# r0..r12: r0,r1,r2,r3 from pattern, r4..r12 fixed distinct
for name, asm in SNIPPETS:
    data = asm_bytes(asm)
    if len(data) % 2: data += b"\x00"
    hws = list(struct.unpack("<%dH" % (len(data)//2), data))
    if name in ("tbb", "tbh"):
        hws += [0xBF00, 0xBF00]
    steps = 1
    if name.startswith("ite_"): steps = 3
    if name.startswith("ittt_"): steps = 4
    if name.startswith("it_"): steps = 2
    for pi,(a,b,c,r3v,shift) in enumerate(pats):
        for fi,flags in enumerate((0x00000000, 0xF0000000)):
            regs = [a,b,c,r3v, 5,6,7,8, 9,10,11,12,13]
            mem = {}
            if name in ("tbb","tbh"):
                mem[0x20000400] = 0x00000000
                regs[1] = 0
                regs[3] = 0x20000400
            if name in ("tbb_pc","tbh_pc"):
                regs[1] = 0
            if "ldr" in name or "str" in name or "ldm" in name or "stm" in name or "push" in name or "pop" in name or name in ("ldrex","strex","ldaex","stlex","lda","stl"):
                mem[0x20000400] = 0x11223344
                mem[0x20000404] = 0x55667788
                mem[0x20000408] = 0x99AABBCC
                mem[0x2000040c] = 0xDDEEFF00
                mem[0x20000410] = 0x01234567
                mem[0x20000418] = 0x89ABCDEF
                mem[0x20000420] = 0xFEDCBA98
                mem[0x20000424] = 0x76543210
                if regs[3] != 0x20000400:
                    regs[3] = 0x20000400
                if name in ("ldr16_reg","ldr_w_reg","str_w_reg"):
                    regs[1] = 1
            cases.append((name, hws, regs, flags, mem, steps, pi, fi))

with open("internal/m33/corpus_gen_test.go","w") as f:
    f.write("// Code generated by tools/gen_corpus.py; DO NOT EDIT.\n\npackage m33\n\n")
    f.write("func instructionCorpus() []diffCase {\n\treturn []diffCase{\n")
    for (name,hws,regs,flags,mem,steps,pi,fi) in cases:
        hw = ", ".join("0x%04x" % h for h in hws)
        rg = ", ".join("0x%08x" % v for v in regs)
        mems = ", ".join("%d: 0x%08x" % (a,v) for a,v in sorted(mem.items()))
        f.write('\t\t{name: "%s/p%d/f%d", hw: []uint16{%s}, regs: [13]uint32{%s}, cpsr: 0x%08x, mem: map[uint32]uint32{%s}, steps: %d},\n' % (name,pi,fi,hw,rg,flags,mems,steps))
    f.write("\t}\n}\n")
print("cases:", len(cases))
