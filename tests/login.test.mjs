import { describe, expect, it } from "vitest";
import { TestDriver } from "testdriverai/vitest/hooks";

// Sample TestDriver test for the AXIS MCP production site (https://axismcp.app).
// Verifies the Log In page renders its sign-in form. No credentials are used —
// this is a public smoke test that the auth entry point is reachable and renders.
describe("AXIS MCP — login page", () => {
  it("opens the sign-in form from the Log In link", async (context) => {
    const testdriver = TestDriver(context);

    await testdriver.provision.chrome({ url: "https://axismcp.app" });
    await testdriver.wait(3000);

    // Open the login page from the header.
    await testdriver.find('The "Log In" link/button in the top right header').click();
    await testdriver.wait(4000);

    // The sign-in form can render below the fold; bring it into view.
    await testdriver.scroll("down", { amount: 400 });
    await testdriver.wait(1000);

    const assertResult = await testdriver.assert(
      "A sign-in / log-in form is visible with an email input field and a Sign In button",
    );
    expect(assertResult).toBeTruthy();
  });
});
