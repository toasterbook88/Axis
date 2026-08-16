import { describe, expect, it } from "vitest";
import { TestDriver } from "testdriverai/vitest/hooks";

// Sample TestDriver smoke test for the AXIS MCP production site (https://axismcp.app).
// Verifies the public landing page loads and renders its primary hero content.
describe("AXIS MCP — landing page", () => {
  it("loads the landing page with hero headline and primary CTA", async (context) => {
    const testdriver = TestDriver(context);

    await testdriver.provision.chrome({ url: "https://axismcp.app" });

    // Give the marketing page time to render its hero section.
    await testdriver.wait(3000);

    const assertResult = await testdriver.assert(
      'The AXIS MCP landing page is visible with the headline about building iOS apps faster and a "Get Started" button',
    );
    expect(assertResult).toBeTruthy();
  });
});
